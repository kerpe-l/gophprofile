package domain

import "errors"

// Доменные ошибки. Транспорт сравнивает их через errors.Is и сам решает,
// в какой код ответа их перевести; тексты рассчитаны на показ клиенту
// и не содержат внутренних деталей.
var (
	// ErrNotFound — аватар не существует или уже удалён.
	ErrNotFound = errors.New("avatar not found")
	// ErrForbidden — попытка удалить чужой аватар.
	ErrForbidden = errors.New("forbidden")
	// ErrInvalidTransition — запись жива, но перевести её в запрошенный
	// статус из текущего нельзя.
	ErrInvalidTransition = errors.New("invalid status transition")
	// ErrUnsupportedFormat — формат файла не входит в число поддерживаемых.
	ErrUnsupportedFormat = errors.New("unsupported image format")
	// ErrTooLarge — размер файла превышает допустимый.
	ErrTooLarge = errors.New("file too large")
	// ErrImageTooBig — число пикселей изображения превышает допустимое.
	ErrImageTooBig = errors.New("image is too large to process")
	// ErrUnsupportedSize — запрошен размер, в котором миниатюры не создаются.
	ErrUnsupportedSize = errors.New("unsupported size")
)
