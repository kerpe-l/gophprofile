package http_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kerpe-l/gophprofile/internal/domain"
)

func TestUpload(t *testing.T) {
	t.Parallel()

	avatar := completedAvatar()
	avatar.ProcessingStatus = domain.ProcessingStatusPending

	svc := &fakeService{avatar: avatar}
	router := newRouter(t, svc, nil)

	w := do(t, router, uploadRequest(t, formFile{field: "file", filename: "photo.png", content: []byte("image bytes")}))

	require.Equal(t, http.StatusCreated, w.Code)

	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))

	assert.Equal(t, avatar.ID.String(), body["id"])
	assert.Equal(t, testUserID, body["user_id"])
	assert.Equal(t, "/api/v1/avatars/"+avatar.ID.String(), body["url"])
	assert.Equal(t, "processing", body["status"])
	assert.Equal(t, "2026-08-07T10:00:00Z", body["created_at"])

	assert.Equal(t, testUserID, svc.uploadInput.UserID)
	assert.Equal(t, "photo.png", svc.uploadInput.FileName)
	assert.Equal(t, int64(len("image bytes")), svc.uploadInput.Size)
	assert.Equal(t, []byte("image bytes"), svc.uploadBody)
}

// TestUploadFieldNames — файл принимается и под именем image, но при обоих
// именах сразу побеждает file.
func TestUploadFieldNames(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		files    []formFile
		wantBody string
	}{
		{
			name:     "file",
			files:    []formFile{{field: "file", filename: "a.png", content: []byte("from file")}},
			wantBody: "from file",
		},
		{
			name:     "image",
			files:    []formFile{{field: "image", filename: "a.png", content: []byte("from image")}},
			wantBody: "from image",
		},
		{
			name: "both",
			files: []formFile{
				{field: "image", filename: "a.png", content: []byte("from image")},
				{field: "file", filename: "b.png", content: []byte("from file")},
			},
			wantBody: "from file",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			svc := &fakeService{avatar: completedAvatar()}
			router := newRouter(t, svc, nil)

			w := do(t, router, uploadRequest(t, tc.files...))

			require.Equal(t, http.StatusCreated, w.Code)
			assert.Equal(t, tc.wantBody, string(svc.uploadBody))
		})
	}
}

// TestUploadTooLarge — тело обрывается на лимите, а не после вычитывания
// всего, что прислал клиент.
func TestUploadTooLarge(t *testing.T) {
	t.Parallel()

	svc := &fakeService{avatar: completedAvatar()}
	router := newRouter(t, svc, nil)

	oversized := strings.Repeat("x", testMaxBytes+1)
	w := do(t, router, uploadRequest(t, formFile{field: "file", filename: "big.png", content: []byte(oversized)}))

	require.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
	assert.Zero(t, svc.gotRequests)

	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))

	assert.Equal(t, "File too large", body["error"])
	assert.InDelta(t, float64(testMaxBytes), body["max_size"], 0)
}

func TestUploadBadRequests(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		request func(t *testing.T) *http.Request
	}{
		{
			name: "no user id",
			request: func(t *testing.T) *http.Request {
				t.Helper()

				r := uploadRequest(t, formFile{field: "file", filename: "a.png", content: []byte("x")})
				r.Header.Del("X-User-ID")

				return r
			},
		},
		{
			name: "no file",
			request: func(t *testing.T) *http.Request {
				t.Helper()

				return uploadRequest(t)
			},
		},
		{
			name: "not multipart",
			request: func(t *testing.T) *http.Request {
				t.Helper()

				r := request(t, http.MethodPost, "/api/v1/avatars")
				r.Header.Set("X-User-ID", testUserID)

				return r
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			svc := &fakeService{avatar: completedAvatar()}
			router := newRouter(t, svc, nil)

			w := do(t, router, tc.request(t))

			require.Equal(t, http.StatusBadRequest, w.Code)
			assert.Zero(t, svc.gotRequests)
		})
	}
}

// TestErrorMapping — статусы выбирает маппинг доменных ошибок, а не хендлеры.
func TestErrorMapping(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantError  string
	}{
		{
			name:       "unsupported format",
			err:        domain.ErrUnsupportedFormat,
			wantStatus: http.StatusBadRequest,
			wantError:  "Invalid file format",
		},
		{
			name:       "image too big",
			err:        domain.ErrImageTooBig,
			wantStatus: http.StatusBadRequest,
			wantError:  "Invalid file format",
		},
		{
			name:       "file too large",
			err:        domain.ErrTooLarge,
			wantStatus: http.StatusRequestEntityTooLarge,
			wantError:  "File too large",
		},
		{
			name:       "invalid transition",
			err:        domain.ErrInvalidTransition,
			wantStatus: http.StatusConflict,
			wantError:  "Avatar state changed",
		},
		{
			name:       "unknown failure",
			err:        assert.AnError,
			wantStatus: http.StatusInternalServerError,
			wantError:  "Internal error",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			svc := &fakeService{err: tc.err}
			router := newRouter(t, svc, nil)

			w := do(t, router, uploadRequest(t, formFile{field: "file", filename: "a.png", content: []byte("x")}))

			require.Equal(t, tc.wantStatus, w.Code)

			var body map[string]any
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))

			assert.Equal(t, tc.wantError, body["error"])
			// Внутренние детали наружу не уходят.
			assert.NotContains(t, w.Body.String(), assert.AnError.Error())
		})
	}
}

func TestAvatarMetadata(t *testing.T) {
	t.Parallel()

	avatar := completedAvatar()
	svc := &fakeService{avatar: avatar}
	router := newRouter(t, svc, nil)

	w := do(t, router, request(t, http.MethodGet, "/api/v1/avatars/"+avatar.ID.String()+"/metadata"))

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, avatar.ID, svc.gotID)

	var body struct {
		ID         string `json:"id"`
		UserID     string `json:"user_id"`
		FileName   string `json:"file_name"`
		MimeType   string `json:"mime_type"`
		Size       int64  `json:"size"`
		Dimensions struct {
			Width  int `json:"width"`
			Height int `json:"height"`
		} `json:"dimensions"`
		Thumbnails []struct {
			Size string `json:"size"`
			URL  string `json:"url"`
		} `json:"thumbnails"`
	}

	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))

	assert.Equal(t, avatar.ID.String(), body.ID)
	assert.Equal(t, avatar.FileName, body.FileName)
	assert.Equal(t, avatar.MimeType, body.MimeType)
	assert.Equal(t, avatar.SizeBytes, body.Size)
	assert.Equal(t, avatar.Width, body.Dimensions.Width)
	assert.Equal(t, avatar.Height, body.Dimensions.Height)

	require.Len(t, body.Thumbnails, 1)
	assert.Equal(t, "100x100", body.Thumbnails[0].Size)
	assert.Equal(t, "/api/v1/avatars/"+avatar.ID.String()+"?size=100x100", body.Thumbnails[0].URL)
}

// TestAvatarMetadataWithoutThumbnails — до конца обработки миниатюр в ответе
// нет: ссылка на несозданную обещала бы размер, которого клиент не получит.
func TestAvatarMetadataWithoutThumbnails(t *testing.T) {
	t.Parallel()

	avatar := completedAvatar()
	avatar.ProcessingStatus = domain.ProcessingStatusProcessing

	svc := &fakeService{avatar: avatar}
	router := newRouter(t, svc, nil)

	w := do(t, router, request(t, http.MethodGet, "/api/v1/avatars/"+avatar.ID.String()+"/metadata"))

	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"thumbnails":[]`)
}

func TestAvatarMetadataNotFound(t *testing.T) {
	t.Parallel()

	svc := &fakeService{err: domain.ErrNotFound}
	router := newRouter(t, svc, nil)

	w := do(t, router, request(t, http.MethodGet, "/api/v1/avatars/"+completedAvatar().ID.String()+"/metadata"))

	require.Equal(t, http.StatusNotFound, w.Code)
	assert.Contains(t, w.Body.String(), "Avatar not found")
}

func TestAvatarInvalidID(t *testing.T) {
	t.Parallel()

	svc := &fakeService{avatar: completedAvatar()}
	router := newRouter(t, svc, nil)

	w := do(t, router, request(t, http.MethodGet, "/api/v1/avatars/not-a-uuid/metadata"))

	require.Equal(t, http.StatusBadRequest, w.Code)
	assert.Zero(t, svc.gotRequests)
}

func TestDeleteAvatar(t *testing.T) {
	t.Parallel()

	avatar := completedAvatar()
	svc := &fakeService{}
	router := newRouter(t, svc, nil)

	r := request(t, http.MethodDelete, "/api/v1/avatars/"+avatar.ID.String())
	r.Header.Set("X-User-ID", testUserID)

	w := do(t, router, r)

	require.Equal(t, http.StatusNoContent, w.Code)
	assert.Empty(t, w.Body.String())
	assert.Equal(t, avatar.ID, svc.gotID)
	assert.Equal(t, testUserID, svc.gotRequeste)
}

func TestDeleteAvatarFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		userID     string
		err        error
		wantStatus int
		wantCalled bool
	}{
		{
			name:       "foreign avatar",
			userID:     testUserID,
			err:        domain.ErrForbidden,
			wantStatus: http.StatusForbidden,
			wantCalled: true,
		},
		{
			name:       "already deleted",
			userID:     testUserID,
			err:        domain.ErrNotFound,
			wantStatus: http.StatusNotFound,
			wantCalled: true,
		},
		{
			name:       "no user id",
			userID:     "",
			wantStatus: http.StatusBadRequest,
			wantCalled: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			svc := &fakeService{err: tc.err}
			router := newRouter(t, svc, nil)

			r := request(t, http.MethodDelete, "/api/v1/avatars/"+completedAvatar().ID.String())
			if tc.userID != "" {
				r.Header.Set("X-User-ID", tc.userID)
			}

			w := do(t, router, r)

			require.Equal(t, tc.wantStatus, w.Code)
			assert.Equal(t, tc.wantCalled, svc.gotRequests > 0)
		})
	}
}
