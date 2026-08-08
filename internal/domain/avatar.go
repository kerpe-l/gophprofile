package domain

import (
	"time"

	"github.com/google/uuid"
)

// Avatar — запись об аватаре: метаданные оригинала, ключи в хранилище
// и состояние обработки. Мягко удалённые записи чтения не возвращают,
// поэтому времени удаления в модели нет.
type Avatar struct {
	ID        uuid.UUID
	UserID    string
	FileName  string
	MimeType  string
	SizeBytes int64
	Width     int
	Height    int
	S3Key     string
	// ThumbnailKeys пусты, пока обработка не завершилась успехом.
	ThumbnailKeys    map[ThumbnailSize]string
	UploadStatus     UploadStatus
	ProcessingStatus ProcessingStatus
	// RetryCount — сколько раз обработка уходила на повтор.
	RetryCount int
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Thumbnail возвращает ключ миниатюры, если обработка завершена и размер
// доступен.
func (a Avatar) Thumbnail(size ThumbnailSize) (string, bool) {
	if a.ProcessingStatus != ProcessingStatusCompleted {
		return "", false
	}

	key, ok := a.ThumbnailKeys[size]

	return key, ok
}

// NewAvatar — данные для создания записи об аватаре. Статусы, счётчик попыток
// и временные метки проставляет база; идентификатор задаёт вызывающий, из него
// строится S3Key.
type NewAvatar struct {
	ID        uuid.UUID
	UserID    string
	FileName  string
	MimeType  string
	SizeBytes int64
	Width     int
	Height    int
	S3Key     string
}
