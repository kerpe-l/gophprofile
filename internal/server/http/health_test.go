package http_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	serverhttp "github.com/kerpe-l/gophprofile/internal/server/http"
)

func TestHealth(t *testing.T) {
	t.Parallel()

	errUnavailable := errors.New("connection refused")

	tests := []struct {
		name       string
		checks     map[string]serverhttp.Checker
		wantStatus int
		wantBody   map[string]string
	}{
		{
			name: "all dependencies are up",
			checks: map[string]serverhttp.Checker{
				serverhttp.ComponentDB:     fakeChecker{},
				serverhttp.ComponentS3:     fakeChecker{},
				serverhttp.ComponentBroker: fakeChecker{},
			},
			wantStatus: http.StatusOK,
			wantBody:   map[string]string{"db": "ok", "s3": "ok", "broker": "ok"},
		},
		{
			name: "storage is down",
			checks: map[string]serverhttp.Checker{
				serverhttp.ComponentDB:     fakeChecker{},
				serverhttp.ComponentS3:     fakeChecker{err: errUnavailable},
				serverhttp.ComponentBroker: fakeChecker{},
			},
			wantStatus: http.StatusServiceUnavailable,
			wantBody:   map[string]string{"db": "ok", "s3": "unavailable", "broker": "ok"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			router := newRouter(t, &fakeService{}, tc.checks)

			w := do(t, router, request(t, http.MethodGet, "/health"))

			require.Equal(t, tc.wantStatus, w.Code)

			var body struct {
				Status     string            `json:"status"`
				Components map[string]string `json:"components"`
			}

			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))

			assert.Equal(t, tc.wantBody, body.Components)

			if tc.wantStatus == http.StatusOK {
				assert.Equal(t, "ok", body.Status)
			} else {
				assert.Equal(t, "degraded", body.Status)
			}

			// Причина отказа зависимости наружу не уходит.
			assert.NotContains(t, w.Body.String(), errUnavailable.Error())
		})
	}
}
