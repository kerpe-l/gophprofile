package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kerpe-l/gophprofile/internal/metrics"
)

func TestMetricsServerRoutes(t *testing.T) {
	t.Parallel()

	srv := metricsServer(":0", metrics.NewRegistry())

	tests := []struct {
		name   string
		method string
		target string
		want   int
	}{
		{name: "liveness", method: http.MethodGet, target: "/livez", want: http.StatusOK},
		{name: "metrics", method: http.MethodGet, target: "/metrics", want: http.StatusOK},
		{name: "unknown path", method: http.MethodGet, target: "/health", want: http.StatusNotFound},
		{name: "wrong method", method: http.MethodPost, target: "/livez", want: http.StatusMethodNotAllowed},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			w := httptest.NewRecorder()
			srv.Handler.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), tc.method, tc.target, nil))

			require.Equal(t, tc.want, w.Code)
		})
	}
}
