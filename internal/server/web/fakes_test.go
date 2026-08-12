package web_test

import (
	"bytes"
	"context"
	"html"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/kerpe-l/gophprofile/internal/config"
	"github.com/kerpe-l/gophprofile/internal/domain"
	serverhttp "github.com/kerpe-l/gophprofile/internal/server/http"
	"github.com/kerpe-l/gophprofile/internal/server/service"
	"github.com/kerpe-l/gophprofile/internal/server/web"
)

const (
	testUserID = "alice"
	// testMaxUpload держится маленьким: тест на превышение размера
	// обходится сотнями байт вместо мегабайтов.
	testMaxUpload = 1024
)

// fakeService подменяет сценарии работы с аватарами. Набор методов —
// как у HTTP-слоя: страницы проверяются на собранном роутере, а не
// на хендлерах в отрыве от маршрутов.
type fakeService struct {
	avatar  domain.Avatar
	avatars []domain.Avatar
	content service.Content
	err     error

	uploadInput service.UploadInput
	uploadBody  []byte
	gotUserID   string
	gotSize     domain.ThumbnailSize
	gotID       uuid.UUID
}

func (f *fakeService) Upload(_ context.Context, in service.UploadInput) (domain.Avatar, error) {
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

func (f *fakeService) ListByUser(_ context.Context, userID string) ([]domain.Avatar, error) {
	f.gotUserID = userID

	if f.err != nil {
		return nil, f.err
	}

	return f.avatars, nil
}

func (f *fakeService) AvatarContent(
	_ context.Context, id uuid.UUID, size domain.ThumbnailSize,
) (service.Content, error) {
	f.gotID = id
	f.gotSize = size

	if f.err != nil {
		return service.Content{}, f.err
	}

	return f.content, nil
}

func (f *fakeService) UserAvatarContent(
	_ context.Context, userID string, size domain.ThumbnailSize,
) (service.Content, error) {
	f.gotUserID = userID
	f.gotSize = size

	if f.err != nil {
		return service.Content{}, f.err
	}

	return f.content, nil
}

func (f *fakeService) Metadata(_ context.Context, id uuid.UUID) (domain.Avatar, error) {
	f.gotID = id

	if f.err != nil {
		return domain.Avatar{}, f.err
	}

	return f.avatar, nil
}

func (f *fakeService) Delete(context.Context, uuid.UUID, string) error {
	return f.err
}

func (f *fakeService) DeleteCurrent(context.Context, string, string) error {
	return f.err
}

// newRouter собирает роутер со страницами поверх фейкового сервиса.
func newRouter(t *testing.T, svc *fakeService) http.Handler {
	t.Helper()

	return newRouterWithPages(t, svc, newPages(t, svc))
}

// newRouterWithPages собирает роутер с заданными страницами; nil отключает
// раздел /web.
func newRouterWithPages(t *testing.T, svc *fakeService, pages *web.Handlers) http.Handler {
	t.Helper()

	return serverhttp.New(serverhttp.Deps{
		Service: svc,
		HTTP: config.HTTP{
			ReadTimeout:    time.Minute,
			RequestTimeout: time.Minute,
		},
		MaxUploadBytes: testMaxUpload,
		Web:            pages,
		Log:            slog.New(slog.DiscardHandler),
	})
}

// newPages собирает веб-интерфейс поверх фейкового сервиса.
func newPages(t *testing.T, svc *fakeService) *web.Handlers {
	t.Helper()

	pages, err := web.New(web.Deps{
		Service:        svc,
		MaxUploadBytes: testMaxUpload,
		Log:            slog.New(slog.DiscardHandler),
	})
	require.NoError(t, err)

	return pages
}

// do прогоняет запрос через роутер и возвращает записанный ответ.
func do(t *testing.T, router http.Handler, r *http.Request) *httptest.ResponseRecorder {
	t.Helper()

	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)

	return w
}

// get собирает запрос без тела с контекстом теста.
func get(t *testing.T, target string) *http.Request {
	t.Helper()

	return httptest.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
}

// formField — поле формы загрузки; пустое имя файла означает текстовое поле.
type formField struct {
	name     string
	fileName string
	value    []byte
}

// uploadRequest собирает отправку формы загрузки.
func uploadRequest(t *testing.T, fields ...formField) *http.Request {
	t.Helper()

	var body bytes.Buffer

	form := multipart.NewWriter(&body)

	for _, field := range fields {
		part, err := writePart(form, field)
		require.NoError(t, err)

		_, err = part.Write(field.value)
		require.NoError(t, err)
	}

	require.NoError(t, form.Close())

	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, web.UploadPath, &body)
	r.Header.Set("Content-Type", form.FormDataContentType())

	return r
}

func writePart(form *multipart.Writer, field formField) (io.Writer, error) {
	if field.fileName == "" {
		return form.CreateFormField(field.name)
	}

	return form.CreateFormFile(field.name, field.fileName)
}

// userField — заполненное поле владельца.
func userField(userID string) formField {
	return formField{name: "user_id", value: []byte(userID)}
}

// fileField — приложенное изображение.
func fileField(name string, size int) formField {
	return formField{name: "file", fileName: name, value: bytes.Repeat([]byte("x"), size)}
}

// completedAvatar — аватар с готовыми миниатюрами.
func completedAvatar() domain.Avatar {
	id := uuid.MustParse("11111111-2222-3333-4444-555555555555")

	return domain.Avatar{
		ID:        id,
		UserID:    testUserID,
		FileName:  "portrait.png",
		MimeType:  "image/png",
		SizeBytes: 2048,
		Width:     640,
		Height:    480,
		S3Key:     domain.OriginalKey(id),
		ThumbnailKeys: map[domain.ThumbnailSize]string{
			domain.ThumbnailSmall: domain.ThumbnailKey(id, domain.ThumbnailSmall),
		},
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
		ETag:        "d41d8cd98f00b204e9800998ecf8427e",
		Size:        int64(len(body)),
		MaxAge:      24 * time.Hour,
	}
}

// thumbnailLink достаёт из разметки адрес первой миниатюры.
func thumbnailLink(t *testing.T, page string) string {
	t.Helper()

	match := regexp.MustCompile(`<img class="thumb" src="([^"]+)"`).FindStringSubmatch(page)
	require.Len(t, match, 2)

	return html.UnescapeString(match[1])
}

// pendingAvatar — аватар, миниатюры которого ещё не созданы.
func pendingAvatar() domain.Avatar {
	avatar := completedAvatar()
	avatar.ID = uuid.MustParse("66666666-7777-8888-9999-000000000000")
	avatar.FileName = "selfie.jpg"
	avatar.ThumbnailKeys = nil
	avatar.ProcessingStatus = domain.ProcessingStatusPending
	avatar.CreatedAt = avatar.CreatedAt.Add(-time.Hour)

	return avatar
}
