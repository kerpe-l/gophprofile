package web_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kerpe-l/gophprofile/internal/server/web"
)

func TestStaticAssets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		path        string
		contentType string
		contains    string
	}{
		{
			name:        "stylesheet",
			path:        web.StaticPath + "/style.css",
			contentType: "text/css",
			contains:    ".gallery",
		},
		{
			name:        "preview script",
			path:        web.StaticPath + "/preview.js",
			contentType: "javascript",
			contains:    "createObjectURL",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			w := do(t, newRouter(t, &fakeService{}), get(t, tc.path))

			require.Equal(t, http.StatusOK, w.Code)
			assert.Contains(t, w.Header().Get("Content-Type"), tc.contentType)
			assert.Equal(t, "max-age=300", w.Header().Get("Cache-Control"))
			assert.Contains(t, w.Body.String(), tc.contains)
		})
	}
}

// Без страниц раздел /web не монтируется: сервер собирается и без него.
func TestWebRoutesRequirePages(t *testing.T) {
	t.Parallel()

	router := newRouterWithPages(t, &fakeService{}, nil)

	for _, path := range []string{web.UploadPath, web.GalleryURL(testUserID), web.StaticPath + "/style.css"} {
		assert.Equal(t, http.StatusNotFound, do(t, router, get(t, path)).Code, path)
	}
}
