package domain

import "fmt"

// ThumbnailSize — размер миниатюры. Одно и то же значение служит ключом
// в наборе миниатюр аватара и значением query-параметра size.
type ThumbnailSize string

// Значения ThumbnailSize.
const (
	ThumbnailSmall  ThumbnailSize = "100x100"
	ThumbnailMedium ThumbnailSize = "300x300"
)

// SizeOriginal — значение query-параметра size, означающее оригинал.
// Оригинал не является миниатюрой и своего ThumbnailSize не имеет.
const SizeOriginal = "original"

// Размеры миниатюр в пикселях.
const (
	smallSide  = 100
	mediumSide = 300
)

// ThumbnailSizes возвращает размеры, в которых создаются миниатюры.
func ThumbnailSizes() []ThumbnailSize {
	return []ThumbnailSize{ThumbnailSmall, ThumbnailMedium}
}

// Dimensions возвращает ширину и высоту миниатюры в пикселях.
// Для неизвестного размера — нули.
func (s ThumbnailSize) Dimensions() (width, height int) {
	switch s {
	case ThumbnailSmall:
		return smallSide, smallSide
	case ThumbnailMedium:
		return mediumSide, mediumSide
	default:
		return 0, 0
	}
}

// ParseThumbnailSize разбирает значение query-параметра size. Пустое значение
// и "original" дают пустой размер без ошибки, любое другое —
// ErrUnsupportedSize.
func ParseThumbnailSize(value string) (ThumbnailSize, error) {
	switch value {
	case "", SizeOriginal:
		return "", nil
	case string(ThumbnailSmall):
		return ThumbnailSmall, nil
	case string(ThumbnailMedium):
		return ThumbnailMedium, nil
	default:
		return "", fmt.Errorf("%w: %s", ErrUnsupportedSize, value)
	}
}
