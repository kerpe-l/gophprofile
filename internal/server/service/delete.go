package service

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/kerpe-l/gophprofile/internal/broker"
	"github.com/kerpe-l/gophprofile/internal/domain"
)

// Delete удаляет аватар по идентификатору от имени requesterID.
//
// Чужой аватар — domain.ErrForbidden, несуществующий или уже удалённый —
// domain.ErrNotFound.
func (s *Service) Delete(ctx context.Context, id uuid.UUID, requesterID string) error {
	avatar, err := s.repo.Get(ctx, id)
	if err != nil {
		return fmt.Errorf("delete avatar %s: %w", id, err)
	}

	return s.remove(ctx, avatar, requesterID)
}

// DeleteCurrent удаляет актуальный аватар пользователя — последний созданный
// среди живых. Остальные аватары того же пользователя не затрагиваются.
func (s *Service) DeleteCurrent(ctx context.Context, userID, requesterID string) error {
	avatar, err := s.repo.GetCurrent(ctx, userID)
	if err != nil {
		return fmt.Errorf("delete current avatar of user %s: %w", userID, err)
	}

	return s.remove(ctx, avatar, requesterID)
}

// remove сверяет владельца, помечает запись удалённой и заказывает воркеру
// уборку файлов.
//
// Файлы удаляются событием, а не здесь: запрос на удаление не должен ждать
// хранилища, а повторную доставку события воркер переживает — лишнее удаление
// отсутствующего объекта ошибкой не считается.
func (s *Service) remove(ctx context.Context, avatar domain.Avatar, requesterID string) error {
	if avatar.UserID != requesterID {
		return fmt.Errorf("delete avatar %s: %w", avatar.ID, domain.ErrForbidden)
	}

	if err := s.repo.SoftDelete(ctx, avatar.ID); err != nil {
		return fmt.Errorf("delete avatar %s: %w", avatar.ID, err)
	}

	// Отказ публикации здесь проваливает запрос, в отличие от загрузки:
	// для несостоявшегося события удаления пути починки нет — удалённые записи
	// никто не перебирает, и файлы остались бы в хранилище навсегда и молча.
	// Запись при этом уже помечена удалённой, поэтому повторный запрос даст
	// domain.ErrNotFound, как и любое повторное удаление.
	if err := s.publisher.Publish(ctx, broker.NewDeleteEvent(avatar.ID, storageKeys(avatar))); err != nil {
		return fmt.Errorf("delete avatar %s: %w", avatar.ID, err)
	}

	return nil
}

// storageKeys собирает ключи всех файлов аватара: оригинал и созданные
// миниатюры. Порядок размеров фиксирован — набор ключей не должен зависеть
// от обхода отображения.
func storageKeys(avatar domain.Avatar) []string {
	keys := make([]string, 0, len(avatar.ThumbnailKeys)+1)
	keys = append(keys, avatar.S3Key)

	for _, size := range domain.ThumbnailSizes() {
		if key, ok := avatar.ThumbnailKeys[size]; ok {
			keys = append(keys, key)
		}
	}

	return keys
}
