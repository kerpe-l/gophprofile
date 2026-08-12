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

func TestGallery(t *testing.T) {
	t.Parallel()

	current, older := completedAvatar(), pendingAvatar()
	svc := &fakeService{avatars: []domain.Avatar{current, older}}

	w := do(t, newRouter(t, svc), get(t, web.GalleryURL(testUserID)))

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, testUserID, svc.gotUserID)

	body := w.Body.String()
	assert.Contains(t, body, "/api/v1/avatars/"+current.ID.String()+"?size=100x100")
	assert.Contains(t, body, "/api/v1/avatars/"+older.ID.String())
	assert.Contains(t, body, "portrait.png")
	assert.Contains(t, body, "selfie.jpg")
	assert.Contains(t, body, "2.0 KB")
	assert.Contains(t, body, "640×480")
	assert.Contains(t, body, "2026-08-07 10:00")
	assert.Contains(t, body, "thumbnails ready")
	assert.Contains(t, body, "processing")
	// Список приходит от новых к старым, актуальный аватар — первый.
	assert.Contains(t, body, "current avatar")
	assert.Equal(t, 1, strings.Count(body, "current avatar"))
}

func TestGalleryEmpty(t *testing.T) {
	t.Parallel()

	svc := &fakeService{}
	w := do(t, newRouter(t, svc), get(t, web.GalleryURL("nobody")))

	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "No avatars yet")
	assert.Contains(t, w.Body.String(), `href="/web/upload?user_id=nobody"`)
}

func TestGalleryServiceFailure(t *testing.T) {
	t.Parallel()

	svc := &fakeService{err: assert.AnError}
	w := do(t, newRouter(t, svc), get(t, web.GalleryURL(testUserID)))

	require.Equal(t, http.StatusInternalServerError, w.Code)

	body := w.Body.String()
	assert.Contains(t, body, "Something went wrong")
	// Наружу уходит общая формулировка, детали остаются в логе.
	assert.NotContains(t, body, assert.AnError.Error())
}

// Ссылка на миниатюру со страницы галереи обязана попадать в хендлер раздачи:
// префикс API страницы знают константой.
func TestGalleryThumbnailLinkHitsAPI(t *testing.T) {
	t.Parallel()

	avatar := completedAvatar()
	svc := &fakeService{avatars: []domain.Avatar{avatar}, content: imageContent([]byte("png-bytes"))}
	router := newRouter(t, svc)

	page := do(t, router, get(t, web.GalleryURL(testUserID)))
	require.Equal(t, http.StatusOK, page.Code)

	link := thumbnailLink(t, page.Body.String())

	image := do(t, router, get(t, link))

	require.Equal(t, http.StatusOK, image.Code)
	assert.Equal(t, "png-bytes", image.Body.String())
	assert.Equal(t, avatar.ID, svc.gotID)
	assert.Equal(t, domain.ThumbnailSmall, svc.gotSize)
}
