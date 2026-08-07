package http_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/kerpe-l/gophprofile/internal/config"
	"github.com/kerpe-l/gophprofile/internal/domain"
	"github.com/kerpe-l/gophprofile/internal/logger"
	serverhttp "github.com/kerpe-l/gophprofile/internal/server/http"
	"github.com/kerpe-l/gophprofile/internal/server/service"
)

const (
	testUserID   = "user-42"
	testETag     = "d41d8cd98f00b204e9800998ecf8427e"
	testMaxBytes = 1 << 20
)

// fakeService подменяет сценарии работы с аватарами: возвращает заданный
// результат и запоминает, с чем его позвали.
type fakeService struct {
	avatar  domain.Avatar
	avatars []domain.Avatar
	content service.Content
	err     error
	// panics — хендлер должен уронить обработку: так проверяется middleware.
	panics bool

	uploadInput service.UploadInput
	uploadBody  []byte
	gotID       uuid.UUID
	gotUserID   string
	gotSize     domain.ThumbnailSize
	gotRequeste string
	gotRequests int
	gotContext  context.Context //nolint:containedctx // тест сверяет, что запрос дошёл со своим контекстом
}

func (f *fakeService) Upload(ctx context.Context, in service.UploadInput) (domain.Avatar, error) {
	f.gotRequests++
	f.gotContext = ctx
	f.uploadInput = in

	body, err := io.ReadAll(in.File)
	if err != nil {
		return domain.Avatar{}, err
	}

	f.uploadBody = body

	if f.err != nil {
		return domain.Avatar{}, f.err
	}

	return f.avatar, nil
}

func (f *fakeService) AvatarContent(
	ctx context.Context, id uuid.UUID, size domain.ThumbnailSize,
) (service.Content, error) {
	f.gotRequests++
	f.gotContext = ctx
	f.gotID = id
	f.gotSize = size

	if f.err != nil {
		return service.Content{}, f.err
	}

	return f.content, nil
}

func (f *fakeService) UserAvatarContent(
	ctx context.Context, userID string, size domain.ThumbnailSize,
) (service.Content, error) {
	f.gotRequests++
	f.gotContext = ctx
	f.gotUserID = userID
	f.gotSize = size

	if f.err != nil {
		return service.Content{}, f.err
	}

	return f.content, nil
}

func (f *fakeService) Metadata(ctx context.Context, id uuid.UUID) (domain.Avatar, error) {
	f.gotRequests++
	f.gotContext = ctx
	f.gotID = id

	if f.panics {
		panic("metadata exploded")
	}

	if f.err != nil {
		return domain.Avatar{}, f.err
	}

	return f.avatar, nil
}

func (f *fakeService) ListByUser(ctx context.Context, userID string) ([]domain.Avatar, error) {
	f.gotRequests++
	f.gotContext = ctx
	f.gotUserID = userID

	if f.err != nil {
		return nil, f.err
	}

	return f.avatars, nil
}

func (f *fakeService) Delete(ctx context.Context, id uuid.UUID, requesterID string) error {
	f.gotRequests++
	f.gotContext = ctx
	f.gotID = id
	f.gotRequeste = requesterID

	return f.err
}

func (f *fakeService) DeleteCurrent(ctx context.Context, userID, requesterID string) error {
	f.gotRequests++
	f.gotContext = ctx
	f.gotUserID = userID
	f.gotRequeste = requesterID

	return f.err
}

// fakeChecker — зависимость с заданным исходом проверки.
type fakeChecker struct {
	err error
}

func (c fakeChecker) Ping(context.Context) error {
	return c.err
}

// newRouter собирает роутер поверх фейкового сервиса.
func newRouter(t *testing.T, svc *fakeService, checks map[string]serverhttp.Checker) http.Handler {
	t.Helper()

	return serverhttp.New(serverhttp.Deps{
		Service: svc,
		Checks:  checks,
		HTTP: config.HTTP{
			ReadTimeout:    time.Minute,
			RequestTimeout: time.Minute,
		},
		MaxUploadBytes: testMaxBytes,
		Log:            slog.New(slog.DiscardHandler),
	})
}

// do прогоняет запрос через роутер и возвращает записанный ответ.
func do(t *testing.T, router http.Handler, r *http.Request) *httptest.ResponseRecorder {
	t.Helper()

	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)

	return w
}

// request собирает запрос без тела с контекстом теста.
func request(t *testing.T, method, target string) *http.Request {
	t.Helper()

	return httptest.NewRequestWithContext(t.Context(), method, target, nil)
}

// formFile — файл, вкладываемый в форму загрузки.
type formFile struct {
	field    string
	filename string
	content  []byte
}

// uploadRequest собирает запрос на загрузку с телом multipart/form-data.
func uploadRequest(t *testing.T, files ...formFile) *http.Request {
	t.Helper()

	var body bytes.Buffer

	form := multipart.NewWriter(&body)

	for _, file := range files {
		part, err := form.CreateFormFile(file.field, file.filename)
		require.NoError(t, err)

		_, err = part.Write(file.content)
		require.NoError(t, err)
	}

	require.NoError(t, form.Close())

	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/avatars", &body)
	r.Header.Set("Content-Type", form.FormDataContentType())
	r.Header.Set("X-User-ID", testUserID)

	return r
}

// completedAvatar — аватар с готовыми миниатюрами.
func completedAvatar() domain.Avatar {
	id := uuid.MustParse("11111111-2222-3333-4444-555555555555")

	return domain.Avatar{
		ID:               id,
		UserID:           testUserID,
		FileName:         "avatar.png",
		MimeType:         "image/png",
		SizeBytes:        2048,
		Width:            640,
		Height:           480,
		S3Key:            domain.OriginalKey(id),
		ThumbnailKeys:    map[domain.ThumbnailSize]string{domain.ThumbnailSmall: domain.ThumbnailKey(id, domain.ThumbnailSmall)},
		UploadStatus:     domain.UploadStatusUploaded,
		ProcessingStatus: domain.ProcessingStatusCompleted,
		CreatedAt:        time.Date(2026, time.August, 7, 10, 0, 0, 0, time.UTC),
		UpdatedAt:        time.Date(2026, time.August, 7, 10, 5, 0, 0, time.UTC),
	}
}

// imageContent — готовое к отдаче изображение.
func imageContent(body []byte) service.Content {
	return service.Content{
		Body:        io.NopCloser(bytes.NewReader(body)),
		ContentType: "image/png",
		ETag:        testETag,
		Size:        int64(len(body)),
		MaxAge:      24 * time.Hour,
	}
}

// requestIDOf возвращает идентификатор запроса, дошедший до сервиса.
func requestIDOf(ctx context.Context) string {
	return logger.RequestID(ctx)
}
