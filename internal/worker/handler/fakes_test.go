package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/kerpe-l/gophprofile/internal/broker"
	"github.com/kerpe-l/gophprofile/internal/domain"
	"github.com/kerpe-l/gophprofile/internal/worker/handler"
)

// Имена вызовов в журнале: так проверяется, что ключ попадает в базу только
// после того, как объект оказался в хранилище.
const (
	callGet        = "get"
	callStatus     = "status:"
	callPut        = "put"
	callComplete   = "complete"
	callRetry      = "retry"
	callDeleteMany = "delete_many"
	callThumbnails = "thumbnails"
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

	// avatar — что отдаёт Get.
	avatar domain.Avatar

	getErr    error
	statusErr error
	// failStatusErr — отказ только на переводе в failed; statusErr сорвал бы
	// и начало обработки, до которого дело бы не дошло.
	failStatusErr error
	completeErr   error
	retryErr      error

	statuses   []domain.ProcessingStatus
	completed  map[domain.ThumbnailSize]string
	retryCount int
}

func (r *fakeRepo) Get(_ context.Context, _ uuid.UUID) (domain.Avatar, error) {
	r.log.add(callGet)

	if r.getErr != nil {
		return domain.Avatar{}, r.getErr
	}

	return r.avatar, nil
}

func (r *fakeRepo) SetProcessingStatus(_ context.Context, _ uuid.UUID, status domain.ProcessingStatus) error {
	r.log.add(callStatus + string(status))
	r.statuses = append(r.statuses, status)

	if status == domain.ProcessingStatusFailed && r.failStatusErr != nil {
		return r.failStatusErr
	}

	return r.statusErr
}

func (r *fakeRepo) CompleteProcessing(
	_ context.Context, _ uuid.UUID, thumbnails map[domain.ThumbnailSize]string,
) error {
	r.log.add(callComplete)
	r.completed = thumbnails

	return r.completeErr
}

func (r *fakeRepo) IncrementRetry(_ context.Context, _ uuid.UUID) error {
	r.log.add(callRetry)
	r.retryCount++

	return r.retryErr
}

// storedObject — записанный в хранилище объект.
type storedObject struct {
	key         string
	body        []byte
	contentType string
}

// fakeStorage — хранилище с заранее заданным оригиналом.
type fakeStorage struct {
	log *callLog

	// original — содержимое, которое отдаёт Get.
	original []byte

	getErr    error
	putErr    error
	deleteErr error

	// closed — тело выданного оригинала закрыто.
	closed  bool
	getKeys []string
	puts    []storedObject
	deleted []string
}

func (s *fakeStorage) Get(_ context.Context, key string) (domain.StoredObject, error) {
	s.log.add(callGet)
	s.getKeys = append(s.getKeys, key)

	if s.getErr != nil {
		return domain.StoredObject{}, s.getErr
	}

	return domain.StoredObject{
		Body:        &closeTracker{Reader: bytes.NewReader(s.original), closed: &s.closed},
		ContentType: "image/jpeg",
		Size:        int64(len(s.original)),
	}, nil
}

func (s *fakeStorage) Put(_ context.Context, key string, r io.Reader, _ int64, contentType string) error {
	s.log.add(callPut)

	body, err := io.ReadAll(r)
	if err != nil {
		return err
	}

	s.puts = append(s.puts, storedObject{key: key, body: body, contentType: contentType})

	return s.putErr
}

func (s *fakeStorage) DeleteMany(_ context.Context, keys []string) error {
	s.log.add(callDeleteMany)
	s.deleted = append(s.deleted, keys...)

	return s.deleteErr
}

// closeTracker отмечает, что тело объекта закрыли.
type closeTracker struct {
	io.Reader
	closed *bool
}

func (c *closeTracker) Close() error {
	*c.closed = true

	return nil
}

// fakeProcessor — создание миниатюр с заданным исходом.
type fakeProcessor struct {
	log *callLog

	thumbnails map[domain.ThumbnailSize][]byte
	err        error

	// source — то, что процессору дали на вход.
	source []byte
}

func (p *fakeProcessor) Thumbnails(
	r io.Reader, sizes []domain.ThumbnailSize,
) (map[domain.ThumbnailSize][]byte, error) {
	p.log.add(callThumbnails)

	source, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}

	p.source = source

	if p.err != nil {
		return nil, p.err
	}

	thumbnails := make(map[domain.ThumbnailSize][]byte, len(sizes))
	for _, size := range sizes {
		thumbnails[size] = p.thumbnails[size]
	}

	return thumbnails, nil
}

// maxOriginalBytes — предел на вычитываемый оригинал в тестах.
const maxOriginalBytes = 10 << 20

// deps — набор фейков с общим журналом вызовов, настроенный на успешный путь.
type deps struct {
	repo      *fakeRepo
	storage   *fakeStorage
	processor *fakeProcessor
	log       *callLog
}

func newDeps(avatar domain.Avatar) *deps {
	log := &callLog{}

	return &deps{
		repo:    &fakeRepo{log: log, avatar: avatar},
		storage: &fakeStorage{log: log, original: []byte("original image")},
		processor: &fakeProcessor{log: log, thumbnails: map[domain.ThumbnailSize][]byte{
			domain.ThumbnailSmall:  []byte("small thumbnail"),
			domain.ThumbnailMedium: []byte("medium thumbnail"),
		}},
		log: log,
	}
}

func newHandler(t *testing.T, d *deps) *handler.Handler {
	t.Helper()

	return handler.New(d.repo, d.storage, d.processor, maxOriginalBytes, 2, slog.New(slog.DiscardHandler))
}

// newAvatar — запись, готовая к обработке: оригинал загружен, миниатюр нет.
func newAvatar() domain.Avatar {
	id := uuid.New()

	return domain.Avatar{
		ID:               id,
		UserID:           "user-1",
		MimeType:         "image/jpeg",
		S3Key:            domain.OriginalKey(id),
		UploadStatus:     domain.UploadStatusUploaded,
		ProcessingStatus: domain.ProcessingStatusPending,
	}
}

// uploadMessage собирает доставленное событие загрузки.
func uploadMessage(t *testing.T, avatar domain.Avatar) broker.Message {
	t.Helper()

	return message(t, broker.RoutingKeyUploaded,
		broker.NewUploadEvent(avatar.ID, avatar.UserID, avatar.S3Key))
}

// deleteMessage собирает доставленное событие удаления.
func deleteMessage(t *testing.T, avatarID uuid.UUID, keys []string) broker.Message {
	t.Helper()

	return message(t, broker.RoutingKeyDeleted, broker.NewDeleteEvent(avatarID, keys))
}

func message(t *testing.T, eventType string, event broker.Event) broker.Message {
	t.Helper()

	body, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}

	return broker.Message{
		Type:      eventType,
		MessageID: event.ID(),
		Body:      body,
		Attempt:   1,
	}
}
