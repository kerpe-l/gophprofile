package broker

import "github.com/google/uuid"

// Event — событие, которое умеет отправить публикатор. Идентификатор и ключ
// маршрутизации знает само событие, а не публикатор.
type Event interface {
	ID() string
	RoutingKey() string
}

// AvatarUploadEvent — оригинал лежит в хранилище, нужны миниатюры.
type AvatarUploadEvent struct {
	MessageID string `json:"message_id"`
	AvatarID  string `json:"avatar_id"`
	UserID    string `json:"user_id"`
	S3Key     string `json:"s3_key"`
}

// NewUploadEvent собирает событие загрузки с новым идентификатором сообщения.
func NewUploadEvent(avatarID uuid.UUID, userID, s3Key string) AvatarUploadEvent {
	return AvatarUploadEvent{
		MessageID: uuid.NewString(),
		AvatarID:  avatarID.String(),
		UserID:    userID,
		S3Key:     s3Key,
	}
}

// ID возвращает идентификатор сообщения.
func (e AvatarUploadEvent) ID() string { return e.MessageID }

// RoutingKey возвращает ключ маршрутизации события загрузки.
func (e AvatarUploadEvent) RoutingKey() string { return RoutingKeyUploaded }

// AvatarDeleteEvent — аватар удалён, файлы надо убрать из хранилища.
type AvatarDeleteEvent struct {
	MessageID string `json:"message_id"`
	AvatarID  string `json:"avatar_id"`
	// S3Keys — ключи оригинала и миниатюр всех размеров, независимо от того,
	// успела ли обработка их создать.
	S3Keys []string `json:"s3_keys"`
}

// NewDeleteEvent собирает событие удаления с новым идентификатором сообщения.
func NewDeleteEvent(avatarID uuid.UUID, s3Keys []string) AvatarDeleteEvent {
	return AvatarDeleteEvent{
		MessageID: uuid.NewString(),
		AvatarID:  avatarID.String(),
		S3Keys:    s3Keys,
	}
}

// ID возвращает идентификатор сообщения.
func (e AvatarDeleteEvent) ID() string { return e.MessageID }

// RoutingKey возвращает ключ маршрутизации события удаления.
func (e AvatarDeleteEvent) RoutingKey() string { return RoutingKeyDeleted }
