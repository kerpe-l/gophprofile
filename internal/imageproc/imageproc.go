// Package imageproc проверяет загружаемые изображения и создаёт миниатюры.
//
// Проверка идёт до полного декодирования: сначала тип по сигнатуре файла,
// затем размеры из заголовка.
//
// Ошибки формата и превышения лимитов — доменные; ошибки ввода-вывода
// возвращаются без изменения классификации.
package imageproc

import (
	"errors"
	"fmt"
	"image"

	// Декодеры регистрируются в реестре пакета image ради image.Decode
	// и image.DecodeConfig.
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"

	_ "golang.org/x/image/webp"

	"github.com/kerpe-l/gophprofile/internal/config"
	"github.com/kerpe-l/gophprofile/internal/domain"
)

// Поддерживаемые типы оригиналов.
const (
	mimeJPEG = "image/jpeg"
	mimePNG  = "image/png"
	mimeWebP = "image/webp"
)

// ThumbnailMimeType — тип содержимого миниатюр. Миниатюры всегда сохраняются
// в JPEG, независимо от формата оригинала.
const ThumbnailMimeType = mimeJPEG

// sniffLen — сколько байт читает http.DetectContentType.
const sniffLen = 512

// Info — что известно об изображении после проверки заголовка.
type Info struct {
	// MimeType определён по сигнатуре файла, а не по имени или заголовку.
	MimeType string
	Width    int
	Height   int
}

// Processor проверяет изображения и создаёт миниатюры в заданных лимитах.
type Processor struct {
	maxPixels   int64
	jpegQuality int
}

// New создаёт процессор с лимитами и качеством JPEG из конфигурации.
func New(cfg config.Image) *Processor {
	return &Processor{
		maxPixels:   cfg.MaxPixels,
		jpegQuality: cfg.JPEGQuality,
	}
}

// readHead читает начало потока, по которому определяется тип содержимого.
// Короткий файл — не ошибка: тип по нему всё равно определится, пусть
// и как неподдерживаемый.
func readHead(r io.Reader) ([]byte, error) {
	head := make([]byte, sniffLen)

	n, err := io.ReadFull(r, head)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, fmt.Errorf("read image header: %w", err)
	}

	return head[:n], nil
}

// checkMime определяет тип содержимого по сигнатуре файла и отвергает всё,
// кроме jpeg, png и webp. Расширение и Content-Type клиента не участвуют.
func checkMime(head []byte) (string, error) {
	// Реестр декодеров шире списка поддерживаемых форматов: библиотека
	// ресайза регистрирует ещё bmp, tiff и gif. Формат ограничивает
	// этот switch, а не то, нашёлся ли для файла декодер.
	switch mime := http.DetectContentType(head); mime {
	case mimeJPEG, mimePNG:
		return mime, nil
	case mimeWebP:
		if isAnimatedWebP(head) {
			return "", fmt.Errorf("%w: animated webp", domain.ErrUnsupportedFormat)
		}

		return mime, nil
	default:
		return "", fmt.Errorf("%w: %s", domain.ErrUnsupportedFormat, mime)
	}
}

// Смещения в заголовке WebP: расширенный формат объявляется чанком VP8X
// сразу за сигнатурой RIFF, а признак анимации — бит в его флагах.
const (
	webpChunkAt = 12
	webpFlagsAt = 20
	webpAnimBit = 0x02
)

// vp8xChunk — идентификатор чанка расширенного формата WebP.
const vp8xChunk = "VP8X"

// isAnimatedWebP сообщает, что файл — анимированный WebP. Отдельная проверка
// нужна потому, что image.DecodeConfig отдаёт размеры первого кадра без
// ошибки, а разваливается только полное декодирование — уже в воркере.
func isAnimatedWebP(head []byte) bool {
	if len(head) <= webpFlagsAt {
		return false
	}

	return string(head[webpChunkAt:webpChunkAt+len(vp8xChunk)]) == vp8xChunk &&
		head[webpFlagsAt]&webpAnimBit != 0
}

// decodeConfig читает заголовок изображения и проверяет число пикселей
// до того, как изображение будет развёрнуто в память.
func (p *Processor) decodeConfig(r io.Reader) (image.Config, error) {
	cfg, _, err := image.DecodeConfig(r)
	if err != nil {
		return image.Config{}, fmt.Errorf("%w: %w", domain.ErrUnsupportedFormat, err)
	}

	if cfg.Width <= 0 || cfg.Height <= 0 {
		return image.Config{}, fmt.Errorf("%w: image has no pixels", domain.ErrUnsupportedFormat)
	}

	if pixels := int64(cfg.Width) * int64(cfg.Height); pixels > p.maxPixels {
		return image.Config{}, fmt.Errorf("%w: %d pixels, limit is %d", domain.ErrImageTooBig, pixels, p.maxPixels)
	}

	return cfg, nil
}
