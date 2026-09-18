package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/kerpe-l/gophprofile/internal/broker"
	"github.com/kerpe-l/gophprofile/internal/domain"
)

// deleted убирает из хранилища файлы удалённого аватара и отмечает уборку
// в базе.
//
// Ключи берутся из события, а не из записи: мягко удалённая запись запросам
// не видна. Повторная доставка безопасна — удаление отсутствующего объекта
// хранилище ошибкой не считает, повторная отметка момент уборки не сдвигает.
func (h *Handler) deleted(ctx context.Context, msg broker.Message) error {
	var event broker.AvatarDeleteEvent
	if err := json.Unmarshal(msg.Body, &event); err != nil {
		return nonRetryable(fmt.Errorf("decode delete event %s: %w", msg.MessageID, err))
	}

	id, err := uuid.Parse(event.AvatarID)
	if err != nil {
		return nonRetryable(fmt.Errorf("parse avatar id %q of message %s: %w", event.AvatarID, msg.MessageID, err))
	}

	if len(event.S3Keys) == 0 {
		return nonRetryable(fmt.Errorf("delete event %s has no keys", msg.MessageID))
	}

	trace.SpanFromContext(ctx).SetAttributes(attribute.String(attrAvatarID, id.String()))

	if err := h.storage.DeleteMany(ctx, event.S3Keys); err != nil {
		return fmt.Errorf("delete files of avatar %s: %w", id, err)
	}

	if err := h.repo.MarkFilesRemoved(ctx, id); err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return nonRetryable(fmt.Errorf("mark files of avatar %s removed: %w", id, err))
		}

		return fmt.Errorf("mark files of avatar %s removed: %w", id, err)
	}

	h.log.InfoContext(ctx, "avatar files deleted",
		slog.String("avatar_id", id.String()),
		slog.Int("keys", len(event.S3Keys)),
	)

	return nil
}
