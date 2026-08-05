package domain

import (
	"time"

	"github.com/google/uuid"
)

// Avatar — запись об аватаре: метаданные оригинала, ключи в хранилище
// и состояние обработки.
//
// Времени удаления в модели нет: все чтения отбрасывают мягко удалённые
// записи, поэтому у вычитанного аватара оно заведомо пустое.
type Avatar struct {
	// ID — идентификатор аватара; из него же строятся ключи хранилища.
	ID uuid.UUID
	// UserID — владелец аватара в терминах вызывающей системы.
	UserID string
	// FileName — имя файла, под которым изображение загрузили.
	FileName string
	// MimeType — тип содержимого, определённый по сигнатуре файла.
	MimeType string
	// SizeBytes — размер оригинала в байтах.
	SizeBytes int64
	// Width — ширина оригинала в пикселях.
	Width int
	// Height — высота оригинала в пикселях.
	Height int
	// S3Key — ключ оригинала в хранилище.
	S3Key string
	// ThumbnailKeys — ключи готовых миниатюр по размерам.
	// Пусто, пока обработка не завершилась успехом.
	ThumbnailKeys map[ThumbnailSize]string
	// UploadStatus — состояние загрузки оригинала.
	UploadStatus UploadStatus
	// ProcessingStatus — состояние создания миниатюр.
	ProcessingStatus ProcessingStatus
	// RetryCount — сколько раз обработка уходила на повтор.
	RetryCount int
	// CreatedAt — время создания записи.
	CreatedAt time.Time
	// UpdatedAt — время последнего изменения записи.
	UpdatedAt time.Time
}

// Thumbnail возвращает ключ готовой миниатюры запрошенного размера.
// Второй результат равен false, пока обработка не завершена: тогда вместо
// миниатюры отдаётся оригинал — для аватарки временно неточный размер лучше,
// чем отсутствие изображения.
func (a Avatar) Thumbnail(size ThumbnailSize) (string, bool) {
	if a.ProcessingStatus != ProcessingStatusCompleted {
		return "", false
	}

	key, ok := a.ThumbnailKeys[size]

	return key, ok
}

// NewAvatar — данные для создания записи об аватаре.
//
// Статусы, счётчик попыток и временные метки проставляет база: новая запись
// всегда начинается с uploading + pending. Идентификатор задаёт вызывающий —
// ключ оригинала в хранилище строится из него и нужен уже при вставке.
type NewAvatar struct {
	// ID — идентификатор создаваемого аватара.
	ID uuid.UUID
	// UserID — владелец аватара.
	UserID string
	// FileName — имя загруженного файла.
	FileName string
	// MimeType — тип содержимого по сигнатуре файла.
	MimeType string
	// SizeBytes — размер оригинала в байтах.
	SizeBytes int64
	// Width — ширина оригинала в пикселях.
	Width int
	// Height — высота оригинала в пикселях.
	Height int
	// S3Key — ключ, по которому оригинал кладётся в хранилище.
	S3Key string
}
