package domain

import "github.com/google/uuid"

// Префиксы ключей в объектном хранилище.
const (
	originalsPrefix  = "originals"
	thumbnailsPrefix = "thumbnails"
)

// OriginalKey возвращает ключ оригинала в хранилище. Имя загруженного файла
// в ключ не попадает: оно приходит от клиента.
func OriginalKey(id uuid.UUID) string {
	return originalsPrefix + "/" + id.String()
}

// ThumbnailKey возвращает ключ миниатюры указанного размера.
func ThumbnailKey(id uuid.UUID, size ThumbnailSize) string {
	return thumbnailsPrefix + "/" + id.String() + "/" + string(size)
}
