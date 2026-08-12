package service_test

import (
	"bytes"
	"context"
	"io"
	"iter"
	"log/slog"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/kerpe-l/gophprofile/internal/broker"
	"github.com/kerpe-l/gophprofile/internal/domain"
	"github.com/kerpe-l/gophprofile/internal/imageproc"
	"github.com/kerpe-l/gophprofile/internal/placeholder"
	"github.com/kerpe-l/gophprofile/internal/server/service"
)

// Имена вызовов в журнале: порядок операций загрузки иначе не проверить.
const (
	callValidate   = "validate"
	callCreate     = "create"
	callGet        = "get"
	callPut        = "put"
	callPublish    = "publish"
	callDelete     = "delete"
	callDeleteMany = "delete-many"
	callStatus     = "status:"
)

// callLog — общий журнал вызовов всех фейков.
type callLog struct {
	mu    sync.Mutex
	calls []string
}

func (l *callLog) add(name string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.calls = append(l.calls, name)
}

func (l *callLog) list() []string {
	l.mu.Lock()
	defer l.mu.Unlock()

	return append([]string(nil), l.calls...)
}

// fakeRepo — репозиторий с заданными ответами и записью того, что с ним делали.
type fakeRepo struct {
	log *callLog

	// avatar — что отдают Get и GetCurrent.
	avatar domain.Avatar
	// list — что отдаёт ListByUser.
	list []domain.Avatar

	createErr error
	getErr    error
	statusErr error
	deleteErr error
	listErr   error

	// onCreate вызывается внутри Create: тест отменяет им контекст ровно
	// в тот момент, когда запись уже создана.
	onCreate func()

	created  domain.NewAvatar
	statuses []domain.UploadStatus
	deleted  []uuid.UUID
}

func (r *fakeRepo) Create(_ context.Context, in domain.NewAvatar) (domain.Avatar, error) {
	r.log.add(callCreate)
	r.created = in

	if r.onCreate != nil {
		r.onCreate()
	}

	if r.createErr != nil {
		return domain.Avatar{}, r.createErr
	}

	return domain.Avatar{
		ID:               in.ID,
		UserID:           in.UserID,
		FileName:         in.FileName,
		MimeType:         in.MimeType,
		SizeBytes:        in.SizeBytes,
		Width:            in.Width,
		Height:           in.Height,
		S3Key:            in.S3Key,
		UploadStatus:     domain.UploadStatusUploading,
		ProcessingStatus: domain.ProcessingStatusPending,
	}, nil
}

func (r *fakeRepo) Get(_ context.Context, _ uuid.UUID) (domain.Avatar, error) {
	r.log.add(callGet)

	if r.getErr != nil {
		return domain.Avatar{}, r.getErr
	}

	return r.avatar, nil
}

func (r *fakeRepo) GetCurrent(_ context.Context, _ string) (domain.Avatar, error) {
	r.log.add(callGet)

	if r.getErr != nil {
		return domain.Avatar{}, r.getErr
	}

	return r.avatar, nil
}

func (r *fakeRepo) ListByUser(_ context.Context, _ string) iter.Seq2[domain.Avatar, error] {
	return func(yield func(domain.Avatar, error) bool) {
		for _, avatar := range r.list {
			if !yield(avatar, nil) {
				return
			}
		}

		if r.listErr != nil {
			yield(domain.Avatar{}, r.listErr)
		}
	}
}

func (r *fakeRepo) SetUploadStatus(_ context.Context, _ uuid.UUID, status domain.UploadStatus) error {
	r.log.add(callStatus + string(status))
	r.statuses = append(r.statuses, status)

	return r.statusErr
}

func (r *fakeRepo) SoftDelete(_ context.Context, id uuid.UUID) error {
	r.log.add(callDelete)
	r.deleted = append(r.deleted, id)

	return r.deleteErr
}

// fakeStorage — хранилище, отдающее заранее заданный объект.
type fakeStorage struct {
	log *callLog

	// body — содержимое, которое отдаёт Get.
	body        []byte
	etag        string
	contentType string

	putErr        error
	getErr        error
	deleteManyErr error

	putKey         string
	putBody        []byte
	putContentType string
	// putCtxErr — состояние контекста в момент записи: им проверяется, что
	// отмена запроса не обрывает уже сделанную работу.
	putCtxErr   error
	getKeys     []string
	deletedKeys []string
}

func (s *fakeStorage) Put(ctx context.Context, key string, r io.Reader, _ int64, contentType string) error {
	s.log.add(callPut)
	s.putKey = key
	s.putCtxErr = ctx.Err()
	s.putContentType = contentType

	body, err := io.ReadAll(r)
	if err != nil {
		return err
	}

	s.putBody = body

	return s.putErr
}

func (s *fakeStorage) Get(_ context.Context, key string) (domain.StoredObject, error) {
	s.log.add(callGet)
	s.getKeys = append(s.getKeys, key)

	if s.getErr != nil {
		return domain.StoredObject{}, s.getErr
	}

	return domain.StoredObject{
		Body:        io.NopCloser(bytes.NewReader(s.body)),
		ETag:        s.etag,
		ContentType: s.contentType,
		Size:        int64(len(s.body)),
	}, nil
}

func (s *fakeStorage) DeleteMany(_ context.Context, keys []string) error {
	s.log.add(callDeleteMany)
	s.deletedKeys = append(s.deletedKeys, keys...)

	return s.deleteManyErr
}

// fakePublisher — публикатор, запоминающий отправленные события.
type fakePublisher struct {
	log *callLog

	err    error
	events []broker.Event
	// ctxErr — состояние контекста в момент публикации: им проверяется, что
	// отмена запроса не обрывает уже сделанную работу.
	ctxErr error
}

func (p *fakePublisher) Publish(ctx context.Context, event broker.Event) error {
	p.log.add(callPublish)
	p.events = append(p.events, event)
	p.ctxErr = ctx.Err()

	return p.err
}

// fakeValidator — проверка изображения с заданным исходом.
type fakeValidator struct {
	log *callLog

	info imageproc.Info
	err  error
}

func (v *fakeValidator) Validate(_ io.ReadSeeker) (imageproc.Info, error) {
	v.log.add(callValidate)

	if v.err != nil {
		return imageproc.Info{}, v.err
	}

	return v.info, nil
}

// deps — набор фейков с общим журналом вызовов, настроенный на успешный путь.
type deps struct {
	repo      *fakeRepo
	storage   *fakeStorage
	publisher *fakePublisher
	validator *fakeValidator
	log       *callLog
}

func newDeps() *deps {
	log := &callLog{}

	return &deps{
		repo: &fakeRepo{log: log},
		storage: &fakeStorage{
			log: log, body: []byte("stored image"),
			etag: "d41d8cd98f00b204", contentType: "image/png",
		},
		publisher: &fakePublisher{log: log},
		validator: &fakeValidator{log: log, info: imageproc.Info{MimeType: "image/png", Width: 800, Height: 600}},
		log:       log,
	}
}

// newService собирает сервис на фейках; заглушка настоящая — она не делает I/O.
func newService(t *testing.T, d *deps) *service.Service {
	t.Helper()

	return service.New(
		d.repo, d.storage, d.publisher, d.validator,
		placeholder.New(), slog.New(slog.DiscardHandler),
	)
}
