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

// failure записывает в базу исход неудачной попытки и возвращает исходную
// ошибку консьюмеру — судьбу самого сообщения решает он.
//
// Неисправимая ошибка и последняя попытка дают один и тот же результат:
// повторов больше не будет, значит обработка провалилась окончательно. Про
// исчерпание попыток знает только консьюмер, про базу — только обработчик,
// поэтому признак последней попытки приезжает в сообщении.
func (h *Handler) failure(ctx context.Context, id uuid.UUID, msg broker.Message, err error) error {
	if errors.Is(err, broker.ErrNonRetryable) || msg.Final {
		h.markFailed(ctx, id)

		return err
	}

	h.countRetry(ctx, id)

	return err
}

// markFailed переводит запись в терминальный статус обработки.
//
// Ошибку перевода возвращать некому: наружу уходит та ошибка, ради которой
// сюда и пришли, и подменять её причиной второго порядка значило бы потерять
// исходную. Запись при этом остаётся в processing, и её подберёт разве что
// повторная загрузка — это лучше, чем потерянная причина отказа.
func (h *Handler) markFailed(ctx context.Context, id uuid.UUID) {
	if err := h.repo.SetProcessingStatus(ctx, id, domain.ProcessingStatusFailed); err != nil {
		h.log.WarnContext(ctx, "mark processing failed",
			slog.Any("error", err), slog.String("avatar_id", id.String()))
	}
}

// countRetry увеличивает счётчик попыток обработки. Счётчик информационный:
// уход в очередь мёртвых решается по числу попыток из заголовков сообщения.
func (h *Handler) countRetry(ctx context.Context, id uuid.UUID) {
	if err := h.repo.IncrementRetry(ctx, id); err != nil {
		h.log.WarnContext(ctx, "count processing retry",
			slog.Any("error", err), slog.String("avatar_id", id.String()))
	}
}
