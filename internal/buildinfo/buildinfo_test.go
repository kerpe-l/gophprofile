package buildinfo_test

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/kerpe-l/gophprofile/internal/buildinfo"
)

func TestGetDefaults(t *testing.T) {
	t.Parallel()

	b := buildinfo.Get()

	assert.Equal(t, "dev", b.Version)
	assert.Equal(t, "unknown", b.Date)
}

func TestLogValueGroup(t *testing.T) {
	t.Parallel()

	value := buildinfo.Build{Version: "v1.2.3", Date: "2026-01-02T03:04:05Z"}.LogValue()

	assert.Equal(t, slog.KindGroup, value.Kind())

	attrs := make(map[string]string, 2)
	for _, attr := range value.Group() {
		attrs[attr.Key] = attr.Value.String()
	}

	assert.Equal(t, map[string]string{"version": "v1.2.3", "date": "2026-01-02T03:04:05Z"}, attrs)
}
