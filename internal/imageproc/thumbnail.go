package imageproc

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"

	"github.com/disintegration/imaging"

	"github.com/kerpe-l/gophprofile/internal/domain"
)

// Thumbnails создаёт миниатюры оригинала во всех запрошенных размерах, готовые
// к загрузке в хранилище: JPEG качества из конфигурации, тип содержимого —
// ThumbnailMimeType. Изображение декодируется один раз на все размеры
// и кадрируется по центру, поэтому результат всегда точно запрошенного размера;
// прозрачность схлопывается в белый фон — JPEG альфа-канала не имеет.
//
// Лимит пикселей проверяется здесь заново, до полного декодирования: оригинал
// приходит из хранилища, а не от Validate. Битый или неподдерживаемый файл
// даёт domain.ErrUnsupportedFormat, слишком большой — domain.ErrImageTooBig,
// неизвестный размер миниатюры — domain.ErrUnsupportedSize.
func (p *Processor) Thumbnails(r io.Reader, sizes []domain.ThumbnailSize) (map[domain.ThumbnailSize][]byte, error) {
	img, err := p.decode(r)
	if err != nil {
		return nil, err
	}

	thumbs := make(map[domain.ThumbnailSize][]byte, len(sizes))

	for _, size := range sizes {
		width, height := size.Dimensions()
		if width <= 0 || height <= 0 {
			return nil, fmt.Errorf("%w: %s", domain.ErrUnsupportedSize, size)
		}

		filled := imaging.Fill(img, width, height, imaging.Center, imaging.Lanczos)

		data, err := p.encode(flatten(filled, width, height))
		if err != nil {
			return nil, fmt.Errorf("encode %s thumbnail: %w", size, err)
		}

		thumbs[size] = data
	}

	return thumbs, nil
}

// decode разворачивает изображение в память, предварительно проверив
// по сигнатуре, что формат поддержан, а по заголовку — что размер приемлем.
// Источник неперематываемый, поэтому прочитанное складывается в буфер
// и склеивается обратно с остатком потока.
func (p *Processor) decode(r io.Reader) (image.Image, error) {
	head, err := readHead(r)
	if err != nil {
		return nil, err
	}

	if _, err = checkMime(head); err != nil {
		return nil, err
	}

	rest := io.MultiReader(bytes.NewReader(head), r)

	var seen bytes.Buffer

	if _, err = p.decodeConfig(io.TeeReader(rest, &seen)); err != nil {
		return nil, err
	}

	img, _, err := image.Decode(io.MultiReader(&seen, rest))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", domain.ErrUnsupportedFormat, err)
	}

	return img, nil
}

// flatten накладывает изображение на белый фон: без явной подложки
// JPEG-энкодер берёт цвет прозрачного пикселя, и фон становится чёрным.
func flatten(img image.Image, width, height int) *image.NRGBA {
	background := imaging.New(width, height, color.White)

	return imaging.Overlay(background, img, image.Pt(0, 0), 1.0)
}

// encode кодирует миниатюру в JPEG.
func (p *Processor) encode(img image.Image) ([]byte, error) {
	var buf bytes.Buffer

	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: p.jpegQuality}); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}
