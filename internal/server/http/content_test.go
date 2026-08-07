package http_test

import (
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kerpe-l/gophprofile/internal/domain"
)

func TestAvatarContent(t *testing.T) {
	t.Parallel()

	avatar := completedAvatar()
	svc := &fakeService{content: imageContent([]byte("png bytes"))}
	router := newRouter(t, svc, nil)

	w := do(t, router, request(t, http.MethodGet, "/api/v1/avatars/"+avatar.ID.String()+"?size=100x100"))

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "png bytes", w.Body.String())

	assert.Equal(t, avatar.ID, svc.gotID)
	assert.Equal(t, domain.ThumbnailSmall, svc.gotSize)

	assert.Equal(t, "image/png", w.Header().Get("Content-Type"))
	assert.Equal(t, "9", w.Header().Get("Content-Length"))
	assert.Equal(t, `"`+testETag+`"`, w.Header().Get("ETag"))
	assert.Equal(t, "max-age=86400", w.Header().Get("Cache-Control"))
	assert.Empty(t, w.Header().Get("X-Avatar-Default"))
}

// TestAvatarContentNotModified — совпавший валидатор экономит тело ответа.
func TestAvatarContentNotModified(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		ifNoneMatch string
		wantStatus  int
	}{
		{name: "exact", ifNoneMatch: `"` + testETag + `"`, wantStatus: http.StatusNotModified},
		{name: "weak", ifNoneMatch: `W/"` + testETag + `"`, wantStatus: http.StatusNotModified},
		{name: "any", ifNoneMatch: "*", wantStatus: http.StatusNotModified},
		{name: "in list", ifNoneMatch: `"other", "` + testETag + `"`, wantStatus: http.StatusNotModified},
		{name: "other", ifNoneMatch: `"other"`, wantStatus: http.StatusOK},
		{name: "absent", ifNoneMatch: "", wantStatus: http.StatusOK},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			svc := &fakeService{content: imageContent([]byte("png bytes"))}
			router := newRouter(t, svc, nil)

			r := request(t, http.MethodGet, "/api/v1/avatars/"+completedAvatar().ID.String())
			if tc.ifNoneMatch != "" {
				r.Header.Set("If-None-Match", tc.ifNoneMatch)
			}

			w := do(t, router, r)

			require.Equal(t, tc.wantStatus, w.Code)
			assert.Equal(t, `"`+testETag+`"`, w.Header().Get("ETag"))

			if tc.wantStatus == http.StatusNotModified {
				assert.Empty(t, w.Body.String())
			} else {
				assert.Equal(t, "png bytes", w.Body.String())
			}
		})
	}
}

func TestAvatarContentNotFound(t *testing.T) {
	t.Parallel()

	svc := &fakeService{err: domain.ErrNotFound}
	router := newRouter(t, svc, nil)

	w := do(t, router, request(t, http.MethodGet, "/api/v1/avatars/"+completedAvatar().ID.String()))

	require.Equal(t, http.StatusNotFound, w.Code)
	assert.Contains(t, w.Body.String(), "Avatar not found")
}

// TestAvatarContentUnsupportedSize — неизвестный размер отвергается, а не
// подменяется молча оригиналом: клиент просил маленький файл.
func TestAvatarContentUnsupportedSize(t *testing.T) {
	t.Parallel()

	svc := &fakeService{content: imageContent([]byte("png bytes"))}
	router := newRouter(t, svc, nil)

	w := do(t, router, request(t, http.MethodGet, "/api/v1/avatars/"+completedAvatar().ID.String()+"?size=50x50"))

	require.Equal(t, http.StatusBadRequest, w.Code)
	assert.Zero(t, svc.gotRequests)
	assert.Contains(t, w.Body.String(), "100x100, 300x300, original")
}

func TestUserAvatarContent(t *testing.T) {
	t.Parallel()

	svc := &fakeService{content: imageContent([]byte("png bytes"))}
	router := newRouter(t, svc, nil)

	w := do(t, router, request(t, http.MethodGet, "/api/v1/users/"+testUserID+"/avatar?size=300x300"))

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, testUserID, svc.gotUserID)
	assert.Equal(t, domain.ThumbnailMedium, svc.gotSize)
	assert.Empty(t, w.Header().Get("X-Avatar-Default"))
}

// TestUserAvatarContentPlaceholder — заглушка помечена заголовком и живёт
// в кешах минуты, а не сутки: иначе первая загрузка аватара не подхватится.
func TestUserAvatarContentPlaceholder(t *testing.T) {
	t.Parallel()

	content := imageContent([]byte("placeholder"))
	content.IsDefault = true
	content.MaxAge = 5 * time.Minute

	svc := &fakeService{content: content}
	router := newRouter(t, svc, nil)

	w := do(t, router, request(t, http.MethodGet, "/api/v1/users/nobody/avatar"))

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "placeholder", w.Body.String())
	assert.Equal(t, "true", w.Header().Get("X-Avatar-Default"))
	assert.Equal(t, "max-age=300", w.Header().Get("Cache-Control"))
}

// TestContentBodyClosed — тело закрывается в любом исходе: до Close заняты
// соединение с хранилищем и горутина чтения внутри клиента.
func TestContentBodyClosed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		ifNoneMatch string
	}{
		{name: "streamed", ifNoneMatch: ""},
		{name: "not modified", ifNoneMatch: `"` + testETag + `"`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			body := &trackedBody{}
			content := imageContent([]byte("png bytes"))
			content.Body = body

			svc := &fakeService{content: content}
			router := newRouter(t, svc, nil)

			r := request(t, http.MethodGet, "/api/v1/avatars/"+completedAvatar().ID.String())
			if tc.ifNoneMatch != "" {
				r.Header.Set("If-None-Match", tc.ifNoneMatch)
			}

			do(t, router, r)

			assert.True(t, body.closed)
		})
	}
}

// trackedBody помнит, закрыли ли его.
type trackedBody struct {
	closed bool
}

func (b *trackedBody) Read([]byte) (int, error) {
	return 0, io.EOF
}

func (b *trackedBody) Close() error {
	b.closed = true

	return nil
}
