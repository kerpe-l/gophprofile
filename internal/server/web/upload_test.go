package web_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kerpe-l/gophprofile/internal/domain"
	"github.com/kerpe-l/gophprofile/internal/server/web"
)

func TestUploadForm(t *testing.T) {
	t.Parallel()

	svc := &fakeService{}
	w := do(t, newRouter(t, svc), get(t, web.UploadPath))

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "text/html; charset=utf-8", w.Header().Get("Content-Type"))

	body := w.Body.String()
	assert.Contains(t, body, `action="/web/upload"`)
	assert.Contains(t, body, `name="user_id"`)
	assert.Contains(t, body, `name="file"`)
	assert.Contains(t, body, "1.0 KB")
	assert.Contains(t, body, "/web/static/preview.js")
}

func TestUploadFormPrefilled(t *testing.T) {
	t.Parallel()

	svc := &fakeService{}
	w := do(t, newRouter(t, svc), get(t, web.UploadPath+"?user_id="+testUserID))

	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `value="alice"`)
	assert.Contains(t, w.Body.String(), `href="/web/gallery/alice"`)
}

// Разметка собирается из значения, пришедшего от клиента, поэтому проверяется
// не только текст ответа, но и отсутствие в нём исполняемого фрагмента.
func TestUploadFormEscapesUserID(t *testing.T) {
	t.Parallel()

	svc := &fakeService{}
	w := do(t, newRouter(t, svc), get(t, web.UploadPath+"?user_id=%3Cscript%3Ealert(1)%3C/script%3E"))

	require.Equal(t, http.StatusOK, w.Code)
	assert.NotContains(t, w.Body.String(), "<script>alert(1)</script>")
	assert.Contains(t, w.Body.String(), "&lt;script&gt;")
}

func TestUploadRedirectsToGallery(t *testing.T) {
	t.Parallel()

	svc := &fakeService{avatar: completedAvatar()}

	w := do(t, newRouter(t, svc), uploadRequest(t,
		userField(testUserID),
		fileField("portrait.png", 128),
	))

	require.Equal(t, http.StatusSeeOther, w.Code)
	assert.Equal(t, "/web/gallery/alice", w.Header().Get("Location"))

	assert.Equal(t, testUserID, svc.uploadInput.UserID)
	assert.Equal(t, "portrait.png", svc.uploadInput.FileName)
	assert.Equal(t, int64(128), svc.uploadInput.Size)
	assert.Len(t, svc.uploadBody, 128)
}

func TestUploadFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		fields  []formField
		svcErr  error
		status  int
		message string
	}{
		{
			name:    "no user id",
			fields:  []formField{fileField("portrait.png", 64)},
			status:  http.StatusBadRequest,
			message: "Enter the user id.",
		},
		{
			name:    "blank user id",
			fields:  []formField{userField("   "), fileField("portrait.png", 64)},
			status:  http.StatusBadRequest,
			message: "Enter the user id.",
		},
		{
			name:    "user id too long",
			fields:  []formField{userField(strings.Repeat("u", 256)), fileField("portrait.png", 64)},
			status:  http.StatusBadRequest,
			message: "User id must be at most 255 characters.",
		},
		{
			name:    "no file",
			fields:  []formField{userField(testUserID)},
			status:  http.StatusBadRequest,
			message: "Choose an image to upload.",
		},
		{
			name:    "file name too long",
			fields:  []formField{userField(testUserID), fileField(strings.Repeat("n", 256)+".png", 64)},
			status:  http.StatusBadRequest,
			message: "File name must be at most 255 characters.",
		},
		{
			name:    "file over the limit",
			fields:  []formField{userField(testUserID), fileField("portrait.png", testMaxUpload+1)},
			status:  http.StatusRequestEntityTooLarge,
			message: "File is larger than 1.0 KB.",
		},
		{
			name:    "unsupported format",
			fields:  []formField{userField(testUserID), fileField("portrait.txt", 64)},
			svcErr:  domain.ErrUnsupportedFormat,
			status:  http.StatusBadRequest,
			message: "Unsupported file format.",
		},
		{
			name:    "image too big",
			fields:  []formField{userField(testUserID), fileField("bomb.png", 64)},
			svcErr:  domain.ErrImageTooBig,
			status:  http.StatusBadRequest,
			message: "Image has too many pixels.",
		},
		{
			name:    "service failure",
			fields:  []formField{userField(testUserID), fileField("portrait.png", 64)},
			svcErr:  assert.AnError,
			status:  http.StatusInternalServerError,
			message: "Something went wrong",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			svc := &fakeService{avatar: completedAvatar(), err: tc.svcErr}
			w := do(t, newRouter(t, svc), uploadRequest(t, tc.fields...))

			require.Equal(t, tc.status, w.Code)
			assert.Empty(t, w.Header().Get("Location"))

			body := w.Body.String()
			assert.Contains(t, body, tc.message)
			// Отказ возвращает ту же форму, а не отдельную страницу.
			assert.Contains(t, body, `action="/web/upload"`)
		})
	}
}

// Введённый владелец переживает отказ: заполнять форму заново не приходится.
func TestUploadKeepsUserIDOnFailure(t *testing.T) {
	t.Parallel()

	svc := &fakeService{err: domain.ErrUnsupportedFormat}

	w := do(t, newRouter(t, svc), uploadRequest(t,
		userField(testUserID),
		fileField("portrait.txt", 64),
	))

	require.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), `value="alice"`)
}

// Тело сверх ограничителя обрывается на разборе формы: отказ приходит раньше,
// чем прочитано хотя бы одно поле, поэтому владельца в форме уже не восстановить.
func TestUploadBodyOverReaderLimit(t *testing.T) {
	t.Parallel()

	svc := &fakeService{}

	w := do(t, newRouter(t, svc), uploadRequest(t,
		userField(testUserID),
		fileField("portrait.png", testMaxUpload+(64<<10)),
	))

	require.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
	assert.Contains(t, w.Body.String(), "File is larger than 1.0 KB.")
	assert.Contains(t, w.Body.String(), `value=""`)
	assert.Empty(t, svc.uploadInput.UserID)
}

// Владелец берётся из тела формы: одноимённый параметр строки запроса
// подменил бы отправленное пользователем значение.
func TestUploadIgnoresQueryUserID(t *testing.T) {
	t.Parallel()

	svc := &fakeService{avatar: completedAvatar()}

	r := uploadRequest(t, userField(testUserID), fileField("portrait.png", 64))
	r.URL.RawQuery = "user_id=mallory"

	w := do(t, newRouter(t, svc), r)

	require.Equal(t, http.StatusSeeOther, w.Code)
	assert.Equal(t, "/web/gallery/alice", w.Header().Get("Location"))
	assert.Equal(t, testUserID, svc.uploadInput.UserID)
}

// Заголовок формы не разбирается как multipart: до сервиса запрос не доходит.
func TestUploadMalformedBody(t *testing.T) {
	t.Parallel()

	svc := &fakeService{}

	r := uploadRequest(t, userField(testUserID))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	w := do(t, newRouter(t, svc), r)

	require.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "The form could not be read.")
	assert.Empty(t, svc.uploadInput.UserID)
}
