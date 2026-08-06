package imageproc_test

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kerpe-l/gophprofile/internal/config"
	"github.com/kerpe-l/gophprofile/internal/domain"
	"github.com/kerpe-l/gophprofile/internal/imageproc"
)

func TestThumbnailsSizes(t *testing.T) {
	t.Parallel()

	sizes := domain.ThumbnailSizes()

	thumbs, err := newProcessor(t).Thumbnails(fixture(t, "photo.jpg"), sizes)
	require.NoError(t, err)
	require.Len(t, thumbs, len(sizes))

	for _, size := range sizes {
		data, ok := thumbs[size]
		require.True(t, ok, "no thumbnail for size %s", size)

		img, format, err := image.Decode(bytes.NewReader(data))
		require.NoError(t, err)
		assert.Equal(t, "jpeg", format)

		wantW, wantH := size.Dimensions()
		assert.Equal(t, wantW, img.Bounds().Dx())
		assert.Equal(t, wantH, img.Bounds().Dy())
	}
}

// TestThumbnailsCropsCentered проверяет кадрирование по центру на фикстуре
// из трёх вертикальных полос 600x200: в квадрат попадает только средняя,
// зелёная. Растяжение вместо кропа дало бы все три цвета, кроп от края —
// красный или синий.
func TestThumbnailsCropsCentered(t *testing.T) {
	t.Parallel()

	thumbs, err := newProcessor(t).Thumbnails(fixture(t, "stripes.png"),
		[]domain.ThumbnailSize{domain.ThumbnailSmall})
	require.NoError(t, err)

	img := decodeJPEG(t, thumbs[domain.ThumbnailSmall])

	for _, pt := range []image.Point{{X: 10, Y: 10}, {X: 50, Y: 50}, {X: 89, Y: 89}} {
		r, g, b := rgb(img.At(pt.X, pt.Y))
		assert.Greater(t, g, uint32(50000), "green channel at %v", pt)
		assert.Less(t, r, uint32(15000), "red channel at %v", pt)
		assert.Less(t, b, uint32(15000), "blue channel at %v", pt)
	}
}

// TestThumbnailsFlattensTransparency проверяет, что прозрачный фон становится
// белым: без явной подложки JPEG-энкодер превратил бы его в чёрную рамку.
func TestThumbnailsFlattensTransparency(t *testing.T) {
	t.Parallel()

	thumbs, err := newProcessor(t).Thumbnails(fixture(t, "transparent.png"),
		[]domain.ThumbnailSize{domain.ThumbnailSmall})
	require.NoError(t, err)

	img := decodeJPEG(t, thumbs[domain.ThumbnailSmall])

	r, g, b := rgb(img.At(2, 2))
	assert.Greater(t, r, uint32(60000), "corner must be white")
	assert.Greater(t, g, uint32(60000), "corner must be white")
	assert.Greater(t, b, uint32(60000), "corner must be white")

	// Непрозрачный квадрат в центре остался тёмным — подложка накрыла
	// только прозрачное.
	r, g, b = rgb(img.At(50, 50))
	assert.Less(t, r, uint32(20000), "center must stay dark")
	assert.Less(t, g, uint32(20000), "center must stay dark")
	assert.Less(t, b, uint32(20000), "center must stay dark")
}

func TestThumbnailsErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		file    string
		sizes   []domain.ThumbnailSize
		wantErr error
	}{
		{
			name: "truncated jpeg", file: "truncated.jpg",
			sizes: domain.ThumbnailSizes(), wantErr: domain.ErrUnsupportedFormat,
		},
		{
			name: "unsupported source format", file: "photo.gif",
			sizes: domain.ThumbnailSizes(), wantErr: domain.ErrUnsupportedFormat,
		},
		{
			name: "animated webp", file: "animated.webp",
			sizes: domain.ThumbnailSizes(), wantErr: domain.ErrUnsupportedFormat,
		},
		{
			// Лимит проверяется по заголовку: 30000x30000 не разворачивается
			// в память, тест завершается мгновенно.
			name: "decompression bomb", file: "bomb.png",
			sizes: domain.ThumbnailSizes(), wantErr: domain.ErrImageTooBig,
		},
		{
			name: "unknown thumbnail size", file: "photo.jpg",
			sizes: []domain.ThumbnailSize{"50x50"}, wantErr: domain.ErrUnsupportedSize,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			thumbs, err := newProcessor(t).Thumbnails(fixture(t, tc.file), tc.sizes)

			require.ErrorIs(t, err, tc.wantErr)
			assert.Nil(t, thumbs)
		})
	}
}

// TestThumbnailsUpscales: оригинал меньше миниатюры — размер результата всё
// равно точный, иначе клиент получит не то, что просил.
func TestThumbnailsUpscales(t *testing.T) {
	t.Parallel()

	thumbs, err := newProcessor(t).Thumbnails(fixture(t, "transparent.png"),
		[]domain.ThumbnailSize{domain.ThumbnailMedium})
	require.NoError(t, err)

	img := decodeJPEG(t, thumbs[domain.ThumbnailMedium])
	assert.Equal(t, 300, img.Bounds().Dx())
	assert.Equal(t, 300, img.Bounds().Dy())
}

func TestThumbnailsQualityFromConfig(t *testing.T) {
	t.Parallel()

	sizes := []domain.ThumbnailSize{domain.ThumbnailMedium}

	low, err := imageproc.New(config.Image{MaxPixels: testMaxPixels, JPEGQuality: 10}).
		Thumbnails(fixture(t, "photo.jpg"), sizes)
	require.NoError(t, err)

	high, err := imageproc.New(config.Image{MaxPixels: testMaxPixels, JPEGQuality: 95}).
		Thumbnails(fixture(t, "photo.jpg"), sizes)
	require.NoError(t, err)

	assert.Less(t, len(low[domain.ThumbnailMedium]), len(high[domain.ThumbnailMedium]))
}

func decodeJPEG(t *testing.T, data []byte) image.Image {
	t.Helper()

	img, err := jpeg.Decode(bytes.NewReader(data))
	require.NoError(t, err)

	return img
}

func rgb(c color.Color) (r, g, b uint32) {
	r, g, b, _ = c.RGBA()

	return r, g, b
}

func BenchmarkThumbnails(b *testing.B) {
	data, err := os.ReadFile(filepath.Join("testdata", "photo.jpg"))
	require.NoError(b, err)

	p := imageproc.New(config.Image{MaxPixels: testMaxPixels, JPEGQuality: testJPEGQuality})
	sizes := domain.ThumbnailSizes()

	b.ReportAllocs()

	for b.Loop() {
		if _, err := p.Thumbnails(bytes.NewReader(data), sizes); err != nil {
			b.Fatal(err)
		}
	}
}
