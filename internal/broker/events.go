package broker

import "github.com/google/uuid"

// Event — событие, которое умеет отправить публикатор.
//
// Идентификатор и ключ маршрутизации знает само событие: иначе публикатор
// пришлось бы учить разбирать типы событий переключателем, который придётся
// править при добавлении каждого нового.
type Event interface {
	// ID — идентификатор сообщения, по нему события различаются в логах
	// на всём пути от публикации до обработки.
	ID() string
	// RoutingKey — ключ, с которым событие уходит в обмен.
	RoutingKey() string
}

// AvatarUploadEvent — оригинал лежит в хранилище, нужны миниатюры.
type AvatarUploadEvent struct {
	// MessageID — идентификатор сообщения.
	MessageID string `json:"message_id"`
	// AvatarID — аватар, для которого создаются миниатюры.
	AvatarID string `json:"avatar_id"`
	// UserID — владелец аватара.
	UserID string `json:"user_id"`
	// S3Key — ключ оригинала в хранилище.
	S3Key string `json:"s3_key"`
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
	// MessageID — идентификатор сообщения.
	MessageID string `json:"message_id"`
	// AvatarID — удалённый аватар.
	AvatarID string `json:"avatar_id"`
	// S3Keys — ключи оригинала и всех созданных миниатюр.
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
