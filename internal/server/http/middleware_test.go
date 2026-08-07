package http_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRequestID — идентификатор запроса доходит и до клиента, и до сервиса:
// по нему связываются ответ и записи в логе.
func TestRequestID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		incoming string
		wantSame bool
	}{
		{name: "from client", incoming: "abc-123", wantSame: true},
		{name: "absent", incoming: "", wantSame: false},
		{name: "too long", incoming: strings.Repeat("x", 65), wantSame: false},
		{name: "not printable", incoming: "id\nwith-newline", wantSame: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			svc := &fakeService{avatar: completedAvatar()}
			router := newRouter(t, svc, nil)

			r := request(t, http.MethodGet, "/api/v1/avatars/"+completedAvatar().ID.String()+"/metadata")
			if tc.incoming != "" {
				r.Header.Set("X-Request-ID", tc.incoming)
			}

			w := do(t, router, r)

			id := w.Header().Get("X-Request-ID")
			require.NotEmpty(t, id)
			assert.Equal(t, id, requestIDOf(svc.gotContext))

			if tc.wantSame {
				assert.Equal(t, tc.incoming, id)
			} else {
				assert.NotEqual(t, tc.incoming, id)
			}
		})
	}
}

// TestRecovering — паника в хендлере отдаёт 500, а не роняет процесс.
func TestRecovering(t *testing.T) {
	t.Parallel()

	svc := &fakeService{panics: true}
	router := newRouter(t, svc, nil)

	w := do(t, router, request(t, http.MethodGet, "/api/v1/avatars/"+completedAvatar().ID.String()+"/metadata"))

	require.Equal(t, http.StatusInternalServerError, w.Code)
	assert.Contains(t, w.Body.String(), "Internal error")
	// Наружу не уходит ни текст паники, ни трассировка.
	assert.NotContains(t, w.Body.String(), "metadata exploded")
}

// TestUnknownRoute — неизвестный путь отдаёт 404 роутера, а не панику.
func TestUnknownRoute(t *testing.T) {
	t.Parallel()

	router := newRouter(t, &fakeService{}, nil)

	w := do(t, router, request(t, http.MethodGet, "/api/v1/unknown"))

	assert.Equal(t, http.StatusNotFound, w.Code)
}
