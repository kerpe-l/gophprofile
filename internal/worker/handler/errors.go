package handler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"github.com/kerpe-l/gophprofile/internal/broker"
	"github.com/kerpe-l/gophprofile/internal/domain"
)

// nonRetryable помечает ошибку как неисправимую повтором: событие с такой
// ошибкой подтверждается сразу, минуя лестницу повторов.
func nonRetryable(err error) error {
	return fmt.Errorf("%w: %w", broker.ErrNonRetryable, err)
}

// failure записывает в базу исход неудачной попытки и возвращает консьюмеру
// ошибку, по которой тот решает судьбу сообщения. Неисправимая ошибка
// и последняя попытка равнозначны: повторов больше не будет.
//
// Если терминальный статус записать не удалось, возвращается уже повторяемая
// ошибка: неисправимую консьюмер подтверждает и забывает, и без повтора запись
// осталась бы в processing без единого следа о причине.
func (h *Handler) failure(ctx context.Context, id uuid.UUID, msg broker.Message, err error) error {
	if !errors.Is(err, broker.ErrNonRetryable) && !msg.Final {
		h.countRetry(ctx, id)

		return err
	}

	markErr := h.repo.SetProcessingStatus(ctx, id, domain.ProcessingStatusFailed)

	switch {
	case markErr == nil,
		errors.Is(markErr, domain.ErrNotFound),
		errors.Is(markErr, domain.ErrInvalidTransition):
		return err

	case msg.Final:
		h.log.ErrorContext(ctx, "mark processing failed",
			slog.Any("error", markErr), slog.String("avatar_id", id.String()))

		return err

	default:
		return fmt.Errorf("mark avatar %s as failed (cause: %s): %w", id, err.Error(), markErr)
	}
}

// countRetry увеличивает счётчик попыток. Счётчик информационный: уход
// в очередь мёртвых решается по заголовкам сообщения.
func (h *Handler) countRetry(ctx context.Context, id uuid.UUID) {
	if err := h.repo.IncrementRetry(ctx, id); err != nil {
		h.log.WarnContext(ctx, "count processing retry",
			slog.Any("error", err), slog.String("avatar_id", id.String()))
	}
}
