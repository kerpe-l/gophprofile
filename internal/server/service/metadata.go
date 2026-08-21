package service

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/kerpe-l/gophprofile/internal/domain"
	"github.com/kerpe-l/gophprofile/internal/observability"
)

// Metadata возвращает метаданные аватара.
// Отсутствие живой записи — domain.ErrNotFound.
func (s *Service) Metadata(ctx context.Context, id uuid.UUID) (domain.Avatar, error) {
	ctx, span := s.tracer.Start(ctx, "service.metadata",
		trace.WithAttributes(attribute.String(attrAvatarID, id.String())))
	defer span.End()

	avatar, err := s.repo.Get(ctx, id)
	if err != nil {
		return domain.Avatar{}, observability.SpanError(span, fmt.Errorf("get metadata of avatar %s: %w", id, err))
	}

	return avatar, nil
}

// ListByUser возвращает живые аватары пользователя от новых к старым.
// Пустой список — не ошибка.
//
// Перебор собирается в срез: вызывающему нужен весь список сразу, чтобы
// отдать его одним ответом.
func (s *Service) ListByUser(ctx context.Context, userID string) ([]domain.Avatar, error) {
	ctx, span := s.tracer.Start(ctx, "service.list_by_user",
		trace.WithAttributes(attribute.String(attrUserID, userID)))
	defer span.End()

	var avatars []domain.Avatar

	for avatar, err := range s.repo.ListByUser(ctx, userID) {
		if err != nil {
			return nil, observability.SpanError(span, fmt.Errorf("list avatars of user %s: %w", userID, err))
		}

		avatars = append(avatars, avatar)
	}

	return avatars, nil
}
