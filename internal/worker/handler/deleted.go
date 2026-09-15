package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/kerpe-l/gophprofile/internal/broker"
)

// deleted убирает из хранилища файлы удалённого аватара.
//
// В базу сценарий не ходит: запись помечена удалённой и запросам не видна,
// а ключи оригинала и всех созданных миниатюр перечислены в самом событии.
// Повторная доставка безопасна — удаление отсутствующего объекта хранилище
// ошибкой не считает.
func (h *Handler) deleted(ctx context.Context, msg broker.Message) error {
	var event broker.AvatarDeleteEvent
	if err := json.Unmarshal(msg.Body, &event); err != nil {
		return nonRetryable(fmt.Errorf("decode delete event %s: %w", msg.MessageID, err))
	}

	if len(event.S3Keys) == 0 {
		return nonRetryable(fmt.Errorf("delete event %s has no keys", msg.MessageID))
	}

	trace.SpanFromContext(ctx).SetAttributes(attribute.String(attrAvatarID, event.AvatarID))

	if err := h.storage.DeleteMany(ctx, event.S3Keys); err != nil {
		return fmt.Errorf("delete files of avatar %s: %w", event.AvatarID, err)
	}

	h.log.InfoContext(ctx, "avatar files deleted",
		slog.String("avatar_id", event.AvatarID),
		slog.Int("keys", len(event.S3Keys)),
	)

	return nil
}
