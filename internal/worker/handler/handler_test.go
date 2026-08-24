package handler_test

import (
	"bytes"
	"errors"
	"image"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kerpe-l/gophprofile/internal/broker"
	"github.com/kerpe-l/gophprofile/internal/config"
	"github.com/kerpe-l/gophprofile/internal/domain"
	"github.com/kerpe-l/gophprofile/internal/imageproc"
	"github.com/kerpe-l/gophprofile/internal/worker/handler"
)

// errUnavailable — временный отказ зависимости: такая ошибка обязана уйти
// на лестницу повторов, а не в терминальный статус.
var errUnavailable = errors.New("service unavailable")

func TestHandleUploaded(t *testing.T) {
	t.Parallel()

	avatar := newAvatar()
	d := newDeps(avatar)

	require.NoError(t, newHandler(t, d).Handle(t.Context(), uploadMessage(t, avatar)))

	assert.Equal(t, []string{
		callGet,
		callStatus + string(domain.ProcessingStatusProcessing),
		callGet,
		callThumbnails,
		callPut,
		callPut,
		callComplete,
	}, d.log.list())

	// Оригинал берётся по ключу из записи, а не из события.
	assert.Equal(t, []string{avatar.S3Key}, d.storage.getKeys)
	assert.Equal(t, []byte("original image"), d.processor.source)
	assert.True(t, d.storage.closed, "original body must be closed")

	require.Len(t, d.storage.puts, 2)
	assert.Equal(t, storedObject{
		key:         domain.ThumbnailKey(avatar.ID, domain.ThumbnailSmall),
		body:        []byte("small thumbnail"),
		contentType: imageproc.ThumbnailMimeType,
	}, d.storage.puts[0])
	assert.Equal(t, domain.ThumbnailKey(avatar.ID, domain.ThumbnailMedium), d.storage.puts[1].key)

	assert.Equal(t, map[domain.ThumbnailSize]string{
		domain.ThumbnailSmall:  domain.ThumbnailKey(avatar.ID, domain.ThumbnailSmall),
		domain.ThumbnailMedium: domain.ThumbnailKey(avatar.ID, domain.ThumbnailMedium),
	}, d.repo.completed)
	assert.Zero(t, d.repo.retryCount)
}

// Повторная доставка события для аватара, которого уже нет или который уже
// обработан, подтверждается без единой записи в хранилище.
func TestHandleUploadedIdempotent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		setup  func(*deps)
		status domain.ProcessingStatus
	}{
		{
			name:   "already completed",
			status: domain.ProcessingStatusCompleted,
		},
		{
			name:   "already failed",
			status: domain.ProcessingStatusFailed,
		},
		{
			name:  "avatar deleted",
			setup: func(d *deps) { d.repo.getErr = domain.ErrNotFound },
		},
		{
			name:  "processing finished between read and update",
			setup: func(d *deps) { d.repo.statusErr = domain.ErrInvalidTransition },
		},
		{
			name:  "avatar deleted between read and update",
			setup: func(d *deps) { d.repo.statusErr = domain.ErrNotFound },
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			avatar := newAvatar()
			if tc.status != "" {
				avatar.ProcessingStatus = tc.status
			}

			d := newDeps(avatar)
			if tc.setup != nil {
				tc.setup(d)
			}

			require.NoError(t, newHandler(t, d).Handle(t.Context(), uploadMessage(t, avatar)))

			assert.Empty(t, d.storage.puts, "processed avatar must not be written again")
			assert.Nil(t, d.repo.completed)
			assert.NotContains(t, d.repo.statuses, domain.ProcessingStatusFailed)
		})
	}
}

// Неисправимая ошибка подтверждает сообщение и переводит запись в failed:
// прогонять по лестнице повторов то, что не станет целым, незачем.
func TestHandleNonRetryable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		message    func(*testing.T, domain.Avatar) broker.Message
		setup      func(*deps)
		wantFailed bool
	}{
		{
			name: "malformed body",
			message: func(t *testing.T, avatar domain.Avatar) broker.Message {
				t.Helper()

				msg := uploadMessage(t, avatar)
				msg.Body = []byte("{not json")

				return msg
			},
		},
		{
			name: "avatar id is not a uuid",
			message: func(t *testing.T, avatar domain.Avatar) broker.Message {
				t.Helper()

				msg := uploadMessage(t, avatar)
				msg.Body = []byte(`{"message_id":"m-1","avatar_id":"not-a-uuid"}`)

				return msg
			},
		},
		{
			name: "unknown event type",
			message: func(t *testing.T, avatar domain.Avatar) broker.Message {
				t.Helper()

				msg := uploadMessage(t, avatar)
				msg.Type = "avatar.exploded"

				return msg
			},
		},
		{
			name:       "broken image",
			message:    uploadMessage,
			setup:      func(d *deps) { d.processor.err = domain.ErrUnsupportedFormat },
			wantFailed: true,
		},
		{
			name:       "original is gone",
			message:    uploadMessage,
			setup:      func(d *deps) { d.storage.getErr = domain.ErrNotFound },
			wantFailed: true,
		},
		{
			name:    "original is larger than the upload limit",
			message: uploadMessage,
			setup: func(d *deps) {
				d.storage.original = make([]byte, maxOriginalBytes+1)
			},
			wantFailed: true,
		},
		{
			name:       "avatar deleted while processing",
			message:    uploadMessage,
			setup:      func(d *deps) { d.repo.completeErr = domain.ErrNotFound },
			wantFailed: true,
		},
		{
			name: "delete event without keys",
			message: func(t *testing.T, _ domain.Avatar) broker.Message {
				t.Helper()

				return deleteMessage(t, uuid.New(), nil)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			avatar := newAvatar()
			d := newDeps(avatar)

			if tc.setup != nil {
				tc.setup(d)
			}

			err := newHandler(t, d).Handle(t.Context(), tc.message(t, avatar))
			require.ErrorIs(t, err, broker.ErrNonRetryable)

			assert.Zero(t, d.repo.retryCount, "non-retryable failure must not count as a retry")

			if tc.wantFailed {
				assert.Contains(t, d.repo.statuses, domain.ProcessingStatusFailed)
			}
		})
	}
}

// Запись исчезла, пока писались миниатюры: событие удаления их не застало,
// поэтому обработчик убирает их за собой сам.
func TestHandleUploadedRemovesOrphanedThumbnails(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		completeErr error
	}{
		{name: "avatar deleted", completeErr: domain.ErrNotFound},
		{name: "processing finished elsewhere", completeErr: domain.ErrInvalidTransition},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			avatar := newAvatar()
			d := newDeps(avatar)
			d.repo.completeErr = tc.completeErr

			err := newHandler(t, d).Handle(t.Context(), uploadMessage(t, avatar))
			require.ErrorIs(t, err, broker.ErrNonRetryable)

			assert.Equal(t, []string{
				domain.ThumbnailKey(avatar.ID, domain.ThumbnailSmall),
				domain.ThumbnailKey(avatar.ID, domain.ThumbnailMedium),
			}, d.storage.deleted)
		})
	}
}

// Временный отказ зависимости уходит на повтор: статус остаётся рабочим,
// счётчик попыток растёт.
func TestHandleRetryable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		setup func(*deps)
	}{
		{name: "read original", setup: func(d *deps) { d.storage.getErr = errUnavailable }},
		{name: "store thumbnail", setup: func(d *deps) { d.storage.putErr = errUnavailable }},
		{name: "complete processing", setup: func(d *deps) { d.repo.completeErr = errUnavailable }},
		{name: "read avatar", setup: func(d *deps) { d.repo.getErr = errUnavailable }},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			avatar := newAvatar()
			d := newDeps(avatar)
			tc.setup(d)

			err := newHandler(t, d).Handle(t.Context(), uploadMessage(t, avatar))
			require.ErrorIs(t, err, errUnavailable)
			require.NotErrorIs(t, err, broker.ErrNonRetryable)

			assert.Equal(t, 1, d.repo.retryCount)
			assert.NotContains(t, d.repo.statuses, domain.ProcessingStatusFailed)
		})
	}
}

func TestHandleNonRetryableBecomesRetryableWhenStatusNotWritten(t *testing.T) {
	t.Parallel()

	avatar := newAvatar()
	d := newDeps(avatar)
	d.processor.err = domain.ErrUnsupportedFormat
	d.repo.failStatusErr = errUnavailable

	err := newHandler(t, d).Handle(t.Context(), uploadMessage(t, avatar))

	require.ErrorIs(t, err, errUnavailable)
	require.NotErrorIs(t, err, broker.ErrNonRetryable, "иначе сообщение подтвердят и причина потеряется")
	assert.Contains(t, err.Error(), domain.ErrUnsupportedFormat.Error(), "исходная причина обязана остаться в тексте")
}

func TestHandleNonRetryableStaysWhenNothingToWrite(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		markErr error
	}{
		{name: "avatar deleted", markErr: domain.ErrNotFound},
		{name: "already terminal", markErr: domain.ErrInvalidTransition},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			avatar := newAvatar()
			d := newDeps(avatar)
			d.processor.err = domain.ErrUnsupportedFormat
			d.repo.failStatusErr = tc.markErr

			err := newHandler(t, d).Handle(t.Context(), uploadMessage(t, avatar))
			require.ErrorIs(t, err, broker.ErrNonRetryable)
		})
	}
}

func TestHandleFinalAttemptStaysFinalWhenStatusNotWritten(t *testing.T) {
	t.Parallel()

	avatar := newAvatar()
	d := newDeps(avatar)
	d.storage.putErr = errUnavailable
	d.repo.failStatusErr = errors.New("database is down")

	msg := uploadMessage(t, avatar)
	msg.Attempt = 5
	msg.Final = true

	err := newHandler(t, d).Handle(t.Context(), msg)
	require.ErrorIs(t, err, errUnavailable)
	assert.Zero(t, d.repo.retryCount, "the last attempt is not retried")
}

func TestHandleFinalAttempt(t *testing.T) {
	t.Parallel()

	avatar := newAvatar()
	d := newDeps(avatar)
	d.storage.putErr = errUnavailable

	msg := uploadMessage(t, avatar)
	msg.Attempt = 5
	msg.Final = true

	err := newHandler(t, d).Handle(t.Context(), msg)
	require.ErrorIs(t, err, errUnavailable)

	assert.Contains(t, d.repo.statuses, domain.ProcessingStatusFailed)
	assert.Zero(t, d.repo.retryCount, "the last attempt is not retried")
}

func TestHandleUploadedRealImage(t *testing.T) {
	t.Parallel()

	original, err := os.ReadFile(filepath.Join("testdata", "photo.jpg"))
	require.NoError(t, err)

	avatar := newAvatar()
	d := newDeps(avatar)
	d.storage.original = original

	h := handler.New(d.repo, d.storage, imageproc.New(config.Image{
		MaxPixels:   50_000_000,
		JPEGQuality: 85,
	}), maxOriginalBytes, 2, d.metrics, slog.New(slog.DiscardHandler))

	require.NoError(t, h.Handle(t.Context(), uploadMessage(t, avatar)))
	require.Len(t, d.storage.puts, 2)

	for i, size := range domain.ThumbnailSizes() {
		width, height := size.Dimensions()

		cfg, format, err := image.DecodeConfig(bytes.NewReader(d.storage.puts[i].body))
		require.NoError(t, err)
		assert.Equal(t, "jpeg", format)
		assert.Equal(t, width, cfg.Width)
		assert.Equal(t, height, cfg.Height)
	}
}

func TestHandleDeleted(t *testing.T) {
	t.Parallel()

	id := uuid.New()
	keys := []string{
		domain.OriginalKey(id),
		domain.ThumbnailKey(id, domain.ThumbnailSmall),
		domain.ThumbnailKey(id, domain.ThumbnailMedium),
	}

	d := newDeps(newAvatar())

	require.NoError(t, newHandler(t, d).Handle(t.Context(), deleteMessage(t, id, keys)))

	assert.Equal(t, keys, d.storage.deleted)
	// Мягко удалённая запись запросам не видна: ключи едут в самом событии.
	assert.Equal(t, []string{callDeleteMany}, d.log.list())
}

func TestHandleDeletedRetryable(t *testing.T) {
	t.Parallel()

	id := uuid.New()
	d := newDeps(newAvatar())
	d.storage.deleteErr = errUnavailable

	err := newHandler(t, d).Handle(t.Context(), deleteMessage(t, id, []string{domain.OriginalKey(id)}))
	require.ErrorIs(t, err, errUnavailable)
	assert.NotErrorIs(t, err, broker.ErrNonRetryable)
}

// Идемпотентный пропуск и уборка по avatar.deleted попытками обработки
// не считаются: в метрики попадают только реальные попытки.
func TestHandleReportsProcessingMetrics(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		prepare func(avatar *domain.Avatar, d *deps)
		want    []bool
	}{
		{name: "processed", prepare: func(*domain.Avatar, *deps) {}, want: []bool{true}},
		{name: "already completed", prepare: func(avatar *domain.Avatar, d *deps) {
			avatar.ProcessingStatus = domain.ProcessingStatusCompleted
			d.repo.avatar = *avatar
		}, want: nil},
		{name: "original unavailable", prepare: func(_ *domain.Avatar, d *deps) {
			d.storage.getErr = errUnavailable
		}, want: []bool{false}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			avatar := newAvatar()
			d := newDeps(avatar)
			tc.prepare(&avatar, d)

			_ = newHandler(t, d).Handle(t.Context(), uploadMessage(t, avatar))

			assert.Equal(t, tc.want, d.metrics.processed())
		})
	}
}

func TestHandleDeletedNotCountedAsProcessing(t *testing.T) {
	t.Parallel()

	avatar := newAvatar()
	d := newDeps(avatar)

	require.NoError(t, newHandler(t, d).Handle(t.Context(),
		deleteMessage(t, avatar.ID, []string{avatar.S3Key})))

	assert.Empty(t, d.metrics.processed())
}
