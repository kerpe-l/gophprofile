package service

import (
	"context"
	"fmt"
	"log/slog"

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
// Файлы в штатном пути удаляются событием, а не здесь: запрос на удаление
// не должен ждать хранилища, а повторную доставку события воркер переживает —
// лишнее удаление отсутствующего объекта ошибкой не считается.
func (s *Service) remove(ctx context.Context, avatar domain.Avatar, requesterID string) error {
	if avatar.UserID != requesterID {
		return fmt.Errorf("delete avatar %s: %w", avatar.ID, domain.ErrForbidden)
	}

	// Отвалившийся клиент отменяет контекст, а скрытая запись без события
	// уборки — файлы, потерянные навсегда.
	ctx = context.WithoutCancel(ctx)

	if err := s.repo.SoftDelete(ctx, avatar.ID); err != nil {
		return fmt.Errorf("delete avatar %s: %w", avatar.ID, err)
	}

	keys := storageKeys(avatar)

	// Переопубликовать несостоявшееся событие некому: запись уже скрыта.
	// Поэтому при отказе брокера файлы удаляются синхронно; теряются они
	// только при одновременном отказе брокера и хранилища.
	if err := s.publisher.Publish(ctx, broker.NewDeleteEvent(avatar.ID, keys)); err != nil {
		s.log.WarnContext(ctx, "publish delete event, falling back to direct removal",
			slog.Any("error", err), slog.String("avatar_id", avatar.ID.String()))

		if err := s.storage.DeleteMany(ctx, keys); err != nil {
			return fmt.Errorf("delete files of avatar %s: %w", avatar.ID, err)
		}
	}

	return nil
}

// storageKeys собирает ключи оригинала и миниатюр всех размеров. Ключи
// миниатюр строятся из идентификатора, а не берутся из записи: воркер мог
// записать миниатюру уже после её чтения.
func storageKeys(avatar domain.Avatar) []string {
	sizes := domain.ThumbnailSizes()
	keys := make([]string, 0, len(sizes)+1)
	keys = append(keys, avatar.S3Key)

	for _, size := range sizes {
		keys = append(keys, domain.ThumbnailKey(avatar.ID, size))
	}

	return keys
}
