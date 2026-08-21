package service

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/kerpe-l/gophprofile/internal/broker"
	"github.com/kerpe-l/gophprofile/internal/domain"
	"github.com/kerpe-l/gophprofile/internal/observability"
)

// Delete удаляет аватар по идентификатору от имени requesterID.
//
// Чужой аватар — domain.ErrForbidden, несуществующий или уже удалённый —
// domain.ErrNotFound.
func (s *Service) Delete(ctx context.Context, id uuid.UUID, requesterID string) error {
	ctx, span := s.tracer.Start(ctx, "service.delete",
		trace.WithAttributes(attribute.String(attrAvatarID, id.String())))
	defer span.End()

	avatar, err := s.repo.Get(ctx, id)
	if err != nil {
		return observability.SpanError(span, fmt.Errorf("delete avatar %s: %w", id, err))
	}

	if err := s.remove(ctx, avatar, requesterID); err != nil {
		return observability.SpanError(span, err)
	}

	return nil
}

// DeleteCurrent удаляет актуальный аватар пользователя — последний созданный
// среди живых. Остальные аватары того же пользователя не затрагиваются.
func (s *Service) DeleteCurrent(ctx context.Context, userID, requesterID string) error {
	ctx, span := s.tracer.Start(ctx, "service.delete_current",
		trace.WithAttributes(attribute.String(attrUserID, userID)))
	defer span.End()

	avatar, err := s.repo.GetCurrent(ctx, userID)
	if err != nil {
		return observability.SpanError(span, fmt.Errorf("delete current avatar of user %s: %w", userID, err))
	}

	span.SetAttributes(attribute.String(attrAvatarID, avatar.ID.String()))

	if err := s.remove(ctx, avatar, requesterID); err != nil {
		return observability.SpanError(span, err)
	}

	return nil
}

// remove сверяет владельца, помечает запись удалённой и заказывает воркеру
// уборку файлов. Синхронно файлы не удаляются: запрос не должен ждать
// хранилища.
func (s *Service) remove(ctx context.Context, avatar domain.Avatar, requesterID string) error {
	if avatar.UserID != requesterID {
		return fmt.Errorf("delete avatar %s: %w", avatar.ID, domain.ErrForbidden)
	}

	// Скрытая запись без события уборки — файлы, потерянные навсегда,
	// поэтому ушедший клиент не должен обрывать оставшиеся шаги.
	ctx = context.WithoutCancel(ctx)

	if err := s.repo.SoftDelete(ctx, avatar.ID); err != nil {
		return fmt.Errorf("delete avatar %s: %w", avatar.ID, err)
	}

	keys := storageKeys(avatar)

	// Переопубликовать событие некому: запись уже скрыта. При отказе брокера
	// файлы удаляются синхронно.
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
// строятся из идентификатора: воркер мог записать миниатюру уже после
// чтения записи.
func storageKeys(avatar domain.Avatar) []string {
	sizes := domain.ThumbnailSizes()
	keys := make([]string, 0, len(sizes)+1)
	keys = append(keys, avatar.S3Key)

	for _, size := range sizes {
		keys = append(keys, domain.ThumbnailKey(avatar.ID, size))
	}

	return keys
}
