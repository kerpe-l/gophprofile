package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/google/uuid"

	"github.com/kerpe-l/gophprofile/internal/broker"
	"github.com/kerpe-l/gophprofile/internal/domain"
	"github.com/kerpe-l/gophprofile/internal/imageproc"
)

// uploaded создаёт миниатюры загруженного оригинала.
//
// Порядок операций: перевод записи в processing → чтение оригинала →
// миниатюры в хранилище → ключи и completed одним запросом. Ключ миниатюры
// появляется в базе только после того, как сам объект оказался в хранилище:
// обратный порядок разрешил бы раздачу того, чего ещё нет.
func (h *Handler) uploaded(ctx context.Context, msg broker.Message) error {
	var event broker.AvatarUploadEvent
	if err := json.Unmarshal(msg.Body, &event); err != nil {
		return nonRetryable(fmt.Errorf("decode upload event %s: %w", msg.MessageID, err))
	}

	id, err := uuid.Parse(event.AvatarID)
	if err != nil {
		return nonRetryable(fmt.Errorf("parse avatar id %q of message %s: %w", event.AvatarID, msg.MessageID, err))
	}

	avatar, ok, err := h.start(ctx, id)
	if err != nil {
		return h.failure(ctx, id, msg, err)
	}

	if !ok {
		return nil
	}

	if err := h.process(ctx, avatar); err != nil {
		return h.failure(ctx, id, msg, err)
	}

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

// done отвечает, закончена ли обработка окончательно. Терминальные статусы
// повторной обработке не подлежат: за повтор отвечает лестница задержек,
// а не возврат записи в предыдущий статус.
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

	// Порядок размеров фиксирован: набор записанных объектов не должен
	// зависеть от обхода отображения.
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
		// Запись удалили или завершили, пока шли миниатюры. Повторять нечего:
		// следующая попытка застанет то же самое.
		return nonRetryable(fmt.Errorf("complete processing of avatar %s: %w", avatar.ID, err))
	case err != nil:
		return fmt.Errorf("complete processing of avatar %s: %w", avatar.ID, err)
	}

	return nil
}

// render читает оригинал из хранилища и создаёт из него миниатюры.
//
// Ключ берётся из записи, а не из события: где лежит оригинал, знает база,
// а поля события к моменту обработки могли и устареть.
func (h *Handler) render(ctx context.Context, avatar domain.Avatar) (map[domain.ThumbnailSize][]byte, error) {
	original, err := h.readOriginal(ctx, avatar)
	if err != nil {
		return nil, err
	}

	thumbnails, err := h.processor.Thumbnails(bytes.NewReader(original), domain.ThumbnailSizes())
	if err != nil {
		// Ошибки обработки изображений неисправимы по своей природе:
		// перекодировать битый JPEG через пять минут не выйдет.
		return nil, nonRetryable(fmt.Errorf("make thumbnails of avatar %s: %w", avatar.ID, err))
	}

	return thumbnails, nil
}

// readOriginal вычитывает оригинал в память целиком.
//
// Целиком — чтобы обрыв связи с хранилищем не выглядел битым файлом: декодер
// на оборванном потоке отдаёт ту же ошибку формата, что и на испорченном
// изображении, и такая загрузка получила бы терминальный failed вместо повтора.
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
