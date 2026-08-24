package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/kerpe-l/gophprofile/internal/broker"
	"github.com/kerpe-l/gophprofile/internal/domain"
	"github.com/kerpe-l/gophprofile/internal/imageproc"
)

// uploaded создаёт миниатюры загруженного оригинала.
//
// Порядок операций: перевод записи в processing → чтение оригинала →
// миниатюры в хранилище → ключи и completed одним запросом. Ключ появляется
// в базе только после самого объекта, иначе раздача сошлётся на пустоту.
func (h *Handler) uploaded(ctx context.Context, msg broker.Message) error {
	started := time.Now()

	var event broker.AvatarUploadEvent
	if err := json.Unmarshal(msg.Body, &event); err != nil {
		h.metrics.ObserveProcessing(false, time.Since(started))

		return nonRetryable(fmt.Errorf("decode upload event %s: %w", msg.MessageID, err))
	}

	id, err := uuid.Parse(event.AvatarID)
	if err != nil {
		h.metrics.ObserveProcessing(false, time.Since(started))

		return nonRetryable(fmt.Errorf("parse avatar id %q of message %s: %w", event.AvatarID, msg.MessageID, err))
	}

	trace.SpanFromContext(ctx).SetAttributes(attribute.String(attrAvatarID, id.String()))

	avatar, ok, err := h.start(ctx, id)
	if err != nil {
		h.metrics.ObserveProcessing(false, time.Since(started))

		return h.failure(ctx, id, msg, err)
	}

	// Идемпотентный пропуск попыткой обработки не считается и в метрики
	// не попадает.
	if !ok {
		return nil
	}

	if err := h.process(ctx, avatar); err != nil {
		h.metrics.ObserveProcessing(false, time.Since(started))

		return h.failure(ctx, id, msg, err)
	}

	h.metrics.ObserveProcessing(true, time.Since(started))
	h.log.InfoContext(ctx, "avatar processed", slog.String("avatar_id", id.String()))

	return nil
}

// start отбирает запись и начинает обработку. Второй результат равен false,
// когда работы нет: аватар удалён или уже обработан.
func (h *Handler) start(ctx context.Context, id uuid.UUID) (domain.Avatar, bool, error) {
	avatar, err := h.repo.Get(ctx, id)

	switch {
	case errors.Is(err, domain.ErrNotFound):
		// Аватар удалён, пока событие ждало обработки: миниатюры ему уже
		// не нужны, а оригинал уберёт событие удаления.
		h.log.InfoContext(ctx, "upload event skipped: avatar is gone", slog.String("avatar_id", id.String()))

		return domain.Avatar{}, false, nil
	case err != nil:
		return domain.Avatar{}, false, fmt.Errorf("read avatar %s: %w", id, err)
	}

	if done(avatar.ProcessingStatus) {
		h.log.InfoContext(ctx, "upload event skipped: already processed",
			slog.String("avatar_id", id.String()),
			slog.String("processing_status", string(avatar.ProcessingStatus)),
		)

		return domain.Avatar{}, false, nil
	}

	err = h.repo.SetProcessingStatus(ctx, id, domain.ProcessingStatusProcessing)

	switch {
	case errors.Is(err, domain.ErrNotFound), errors.Is(err, domain.ErrInvalidTransition):
		h.log.InfoContext(ctx, "upload event skipped: avatar changed",
			slog.Any("reason", err), slog.String("avatar_id", id.String()))

		return domain.Avatar{}, false, nil
	case err != nil:
		return domain.Avatar{}, false, fmt.Errorf("start processing of avatar %s: %w", id, err)
	}

	return avatar, true, nil
}

// done отвечает, закончена ли обработка окончательно.
func done(status domain.ProcessingStatus) bool {
	switch status {
	case domain.ProcessingStatusCompleted, domain.ProcessingStatusFailed:
		return true
	case domain.ProcessingStatusPending, domain.ProcessingStatusProcessing:
		return false
	default:
		return false
	}
}

// process создаёт миниатюры и завершает обработку записи.
func (h *Handler) process(ctx context.Context, avatar domain.Avatar) error {
	thumbnails, err := h.render(ctx, avatar)
	if err != nil {
		return err
	}

	// Порядок размеров фиксирован: обход map недетерминирован.
	keys := make(map[domain.ThumbnailSize]string, len(thumbnails))

	for _, size := range domain.ThumbnailSizes() {
		data, ok := thumbnails[size]
		if !ok {
			continue
		}

		key := domain.ThumbnailKey(avatar.ID, size)

		err := h.storage.Put(ctx, key, bytes.NewReader(data), int64(len(data)), imageproc.ThumbnailMimeType)
		if err != nil {
			return fmt.Errorf("store %s thumbnail of avatar %s: %w", size, avatar.ID, err)
		}

		keys[size] = key
	}

	err = h.repo.CompleteProcessing(ctx, avatar.ID, keys)

	switch {
	case errors.Is(err, domain.ErrNotFound), errors.Is(err, domain.ErrInvalidTransition):
		// Запись удалили или завершили, пока шли миниатюры.
		h.removeThumbnails(ctx, avatar.ID, keys)

		return nonRetryable(fmt.Errorf("complete processing of avatar %s: %w", avatar.ID, err))
	case err != nil:
		return fmt.Errorf("complete processing of avatar %s: %w", avatar.ID, err)
	}

	return nil
}

// removeThumbnails убирает миниатюры, записанные для исчезнувшей записи.
func (h *Handler) removeThumbnails(ctx context.Context, id uuid.UUID, keys map[domain.ThumbnailSize]string) {
	if len(keys) == 0 {
		return
	}

	orphans := make([]string, 0, len(keys))
	for _, size := range domain.ThumbnailSizes() {
		if key, ok := keys[size]; ok {
			orphans = append(orphans, key)
		}
	}

	if err := h.storage.DeleteMany(ctx, orphans); err != nil {
		h.log.WarnContext(ctx, "remove orphaned thumbnails",
			slog.Any("error", err), slog.String("avatar_id", id.String()))
	}
}

// render читает оригинал из хранилища и создаёт из него миниатюры, занимая
// слот декодирования на всё время работы. Ключ берётся из записи: поля
// события к моменту обработки могли устареть.
func (h *Handler) render(ctx context.Context, avatar domain.Avatar) (map[domain.ThumbnailSize][]byte, error) {
	select {
	case h.decodeSlots <- struct{}{}:
	case <-ctx.Done():
		return nil, fmt.Errorf("wait for decode slot for avatar %s: %w", avatar.ID, ctx.Err())
	}

	defer func() { <-h.decodeSlots }()

	original, err := h.readOriginal(ctx, avatar)
	if err != nil {
		return nil, err
	}

	thumbnails, err := h.processor.Thumbnails(bytes.NewReader(original), domain.ThumbnailSizes())
	if err != nil {
		// Битый JPEG повтором не чинится.
		return nil, nonRetryable(fmt.Errorf("make thumbnails of avatar %s: %w", avatar.ID, err))
	}

	return thumbnails, nil
}

// readOriginal вычитывает оригинал в память целиком: на оборванном потоке
// декодер отдаёт ту же ошибку формата, что и на битом файле, и обрыв связи
// получил бы терминальный failed вместо повтора.
func (h *Handler) readOriginal(ctx context.Context, avatar domain.Avatar) ([]byte, error) {
	object, err := h.storage.Get(ctx, avatar.S3Key)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			// Запись помечена uploaded — значит оригинал клали. Его отсутствие
			// не гонка, а потеря, и повторами она не лечится.
			return nil, nonRetryable(fmt.Errorf("read original of avatar %s: %w", avatar.ID, err))
		}

		return nil, fmt.Errorf("read original of avatar %s: %w", avatar.ID, err)
	}

	defer func() {
		if err := object.Body.Close(); err != nil {
			h.log.WarnContext(ctx, "close original",
				slog.Any("error", err), slog.String("avatar_id", avatar.ID.String()))
		}
	}()

	if object.Size > h.maxOriginalBytes {
		return nil, nonRetryable(fmt.Errorf("original of avatar %s is %d bytes, limit is %d",
			avatar.ID, object.Size, h.maxOriginalBytes))
	}

	data, err := io.ReadAll(io.LimitReader(object.Body, h.maxOriginalBytes))
	if err != nil {
		return nil, fmt.Errorf("read original of avatar %s: %w", avatar.ID, err)
	}

	return data, nil
}
