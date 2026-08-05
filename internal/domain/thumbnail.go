package domain

import "fmt"

// ThumbnailSize — размер миниатюры. Одно и то же значение служит ключом
// в наборе миниатюр аватара и значением query-параметра size.
type ThumbnailSize string

// Значения ThumbnailSize.
const (
	// ThumbnailSmall — миниатюра 100x100.
	ThumbnailSmall ThumbnailSize = "100x100"
	// ThumbnailMedium — миниатюра 300x300.
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
// Функция, а не переменная пакета: содержимое общего слайса вызывающий
// мог бы подменить.
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

// ParseThumbnailSize разбирает значение query-параметра size.
//
// Пустое значение и "original" означают оригинал: возвращается пустой размер
// без ошибки. Любое другое нераспознанное значение — ErrUnsupportedSize:
// молча отдавать оригинал вместо запрошенных 50x50 значит выдавать
// многомегабайтный файл там, где клиент просил маленький.
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
