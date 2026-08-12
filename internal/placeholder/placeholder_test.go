package placeholder_test

import (
	"bytes"
	"image"
	_ "image/png"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kerpe-l/gophprofile/internal/placeholder"
)

func TestNewImage(t *testing.T) {
	t.Parallel()

	p := placeholder.New()

	data, err := io.ReadAll(p.Reader())
	require.NoError(t, err)
	require.Equal(t, p.Size(), int64(len(data)))

	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	require.NoError(t, err)
	assert.Equal(t, "png", format)
	assert.Equal(t, placeholder.ContentType, "image/"+format)

	// Заглушка не мельче самой большой миниатюры: иначе она отдавалась бы
	// растянутой там, где вместо неё ожидали 300x300.
	assert.GreaterOrEqual(t, cfg.Width, 300)
	assert.GreaterOrEqual(t, cfg.Height, 300)
}

// ETag заглушки обязан пережить рестарт, иначе кеши перезапрашивают её
// после каждого деплоя.
func TestETagStable(t *testing.T) {
	t.Parallel()

	first, second := placeholder.New(), placeholder.New()

	assert.NotEmpty(t, first.ETag())
	assert.Equal(t, first.ETag(), second.ETag())
	assert.NotContains(t, first.ETag(), `"`, "quoting is the transport's job")
}

// Экземпляр один на все запросы: второй читатель обязан отдать содержимое
// целиком, а не остаток.
func TestReaderIndependent(t *testing.T) {
	t.Parallel()

	p := placeholder.New()

	first, err := io.ReadAll(p.Reader())
	require.NoError(t, err)

	second, err := io.ReadAll(p.Reader())
	require.NoError(t, err)

	assert.NotEmpty(t, first)
	assert.True(t, bytes.Equal(first, second))
}
