package service

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/kerpe-l/gophprofile/internal/domain"
)

// Metadata возвращает метаданные аватара.
// Отсутствие живой записи — domain.ErrNotFound.
func (s *Service) Metadata(ctx context.Context, id uuid.UUID) (domain.Avatar, error) {
	avatar, err := s.repo.Get(ctx, id)
	if err != nil {
		return domain.Avatar{}, fmt.Errorf("get metadata of avatar %s: %w", id, err)
	}

	return avatar, nil
}

// ListByUser возвращает живые аватары пользователя от новых к старым.
// Пустой список — не ошибка.
//
// Перебор собирается в срез: вызывающему нужен весь список сразу, чтобы
// отдать его одним ответом.
func (s *Service) ListByUser(ctx context.Context, userID string) ([]domain.Avatar, error) {
	var avatars []domain.Avatar

	for avatar, err := range s.repo.ListByUser(ctx, userID) {
		if err != nil {
			return nil, fmt.Errorf("list avatars of user %s: %w", userID, err)
		}

		avatars = append(avatars, avatar)
	}

	return avatars, nil
}
