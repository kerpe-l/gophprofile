package domain_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kerpe-l/gophprofile/internal/domain"
)

func TestParseThumbnailSize(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		value   string
		want    domain.ThumbnailSize
		wantErr error
	}{
		{name: "empty means original", value: "", want: ""},
		{name: "explicit original", value: "original", want: ""},
		{name: "small", value: "100x100", want: domain.ThumbnailSmall},
		{name: "medium", value: "300x300", want: domain.ThumbnailMedium},
		{name: "unknown size", value: "50x50", wantErr: domain.ErrUnsupportedSize},
		{name: "not a size at all", value: "large", wantErr: domain.ErrUnsupportedSize},
		{name: "case matters", value: "100X100", wantErr: domain.ErrUnsupportedSize},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := domain.ParseThumbnailSize(tc.value)

			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				assert.Empty(t, got)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestThumbnailSizeDimensions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		size       domain.ThumbnailSize
		wantWidth  int
		wantHeight int
	}{
		{name: "small", size: domain.ThumbnailSmall, wantWidth: 100, wantHeight: 100},
		{name: "medium", size: domain.ThumbnailMedium, wantWidth: 300, wantHeight: 300},
		{name: "unknown", size: "50x50"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			width, height := tc.size.Dimensions()

			assert.Equal(t, tc.wantWidth, width)
			assert.Equal(t, tc.wantHeight, height)
		})
	}
}

// Каждый размер из набора обязан быть разбираемым и иметь размерности:
// набор, ParseThumbnailSize и Dimensions обязаны сходиться.
func TestThumbnailSizesAreConsistent(t *testing.T) {
	t.Parallel()

	sizes := domain.ThumbnailSizes()
	require.NotEmpty(t, sizes)

	for _, size := range sizes {
		parsed, err := domain.ParseThumbnailSize(string(size))
		require.NoError(t, err)
		assert.Equal(t, size, parsed)

		width, height := size.Dimensions()
		assert.Positive(t, width)
		assert.Positive(t, height)
	}
}
