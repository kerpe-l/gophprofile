package imageproc_test

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kerpe-l/gophprofile/internal/config"
	"github.com/kerpe-l/gophprofile/internal/domain"
	"github.com/kerpe-l/gophprofile/internal/imageproc"
)

// Лимиты, с которыми работают тесты: те же значения, что по умолчанию
// в конфигурации.
const (
	testMaxPixels   = 50_000_000
	testJPEGQuality = 85
)

func newProcessor(t *testing.T) *imageproc.Processor {
	t.Helper()

	return imageproc.New(config.Image{
		MaxUploadBytes: 10 << 20,
		MaxPixels:      testMaxPixels,
		JPEGQuality:    testJPEGQuality,
	})
}

// fixture открывает файл из testdata и закрывает его по окончании теста.
func fixture(t *testing.T, name string) *os.File {
	t.Helper()

	f, err := os.Open(filepath.Join("testdata", name))
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, f.Close()) })

	return f
}

func TestValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		file     string
		wantMime string
		wantW    int
		wantH    int
		wantErr  error
	}{
		{name: "jpeg", file: "photo.jpg", wantMime: "image/jpeg", wantW: 800, wantH: 600},
		{name: "png", file: "photo.png", wantMime: "image/png", wantW: 800, wantH: 600},
		{name: "webp", file: "photo.webp", wantMime: "image/webp", wantW: 320, wantH: 240},
		{
			// Заголовок JPEG цел, поэтому размеры читаются; битые данные
			// обнаружит уже обработчик.
			name: "truncated jpeg passes header checks",
			file: "truncated.jpg", wantMime: "image/jpeg", wantW: 800, wantH: 600,
		},
		{
			// Декодер gif зарегистрирован библиотекой ресайза, но формат
			// не входит в поддерживаемые.
			name: "gif rejected despite registered decoder",
			file: "photo.gif", wantErr: domain.ErrUnsupportedFormat,
		},
		{name: "animated webp", file: "animated.webp", wantErr: domain.ErrUnsupportedFormat},
		{name: "text with png extension", file: "notimage.png", wantErr: domain.ErrUnsupportedFormat},
		{name: "decompression bomb", file: "bomb.png", wantErr: domain.ErrImageTooBig},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			info, err := newProcessor(t).Validate(fixture(t, tc.file))

			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				assert.Equal(t, imageproc.Info{}, info)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tc.wantMime, info.MimeType)
			assert.Equal(t, tc.wantW, info.Width)
			assert.Equal(t, tc.wantH, info.Height)
		})
	}
}

// Инвариант, на который опирается загрузка:
// после Validate поток стоит в начале и отдаёт исходные байты целиком,
// без копии файла в памяти.
func TestValidateRewinds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		file    string
		wantErr bool
	}{
		{name: "valid", file: "photo.jpg"},
		{name: "unsupported format", file: "photo.gif", wantErr: true},
		{name: "too big", file: "bomb.png", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			want, err := os.ReadFile(filepath.Join("testdata", tc.file))
			require.NoError(t, err)

			f := fixture(t, tc.file)

			_, err = newProcessor(t).Validate(f)
			require.Equal(t, tc.wantErr, err != nil)

			got, err := io.ReadAll(f)
			require.NoError(t, err)
			assert.True(t, bytes.Equal(want, got), "stream must be rewound to the beginning")
		})
	}
}

func TestValidateEmptyInput(t *testing.T) {
	t.Parallel()

	_, err := newProcessor(t).Validate(bytes.NewReader(nil))

	require.ErrorIs(t, err, domain.ErrUnsupportedFormat)
}

// Предел берётся из конфигурации, а не зашит в пакет.
func TestValidateRejectsOversized(t *testing.T) {
	t.Parallel()

	p := imageproc.New(config.Image{MaxPixels: 800*600 - 1, JPEGQuality: testJPEGQuality})

	_, err := p.Validate(fixture(t, "photo.jpg"))

	require.ErrorIs(t, err, domain.ErrImageTooBig)
}

func BenchmarkValidate(b *testing.B) {
	data, err := os.ReadFile(filepath.Join("testdata", "photo.jpg"))
	require.NoError(b, err)

	p := imageproc.New(config.Image{MaxPixels: testMaxPixels, JPEGQuality: testJPEGQuality})
	r := bytes.NewReader(data)

	b.ReportAllocs()

	for b.Loop() {
		if _, err := p.Validate(r); err != nil {
			b.Fatal(err)
		}
	}
}
