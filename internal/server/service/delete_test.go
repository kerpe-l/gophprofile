package service_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kerpe-l/gophprofile/internal/broker"
	"github.com/kerpe-l/gophprofile/internal/domain"
)

func TestDelete(t *testing.T) {
	t.Parallel()

	d := newDeps()
	d.repo.avatar = storedAvatar()
	svc := newService(t, d)

	require.NoError(t, svc.Delete(t.Context(), d.repo.avatar.ID, testUserID))

	assert.Equal(t, []uuid.UUID{d.repo.avatar.ID}, d.repo.deleted)
	assert.Equal(t, []string{callGet, callDelete, callPublish}, d.log.list())
}

func TestDeleteCurrent(t *testing.T) {
	t.Parallel()

	d := newDeps()
	d.repo.avatar = storedAvatar()
	svc := newService(t, d)

	require.NoError(t, svc.DeleteCurrent(t.Context(), testUserID, testUserID))

	assert.Equal(t, []uuid.UUID{d.repo.avatar.ID}, d.repo.deleted)
}

// Ключи миниатюр попадают в событие даже до завершения обработки: воркер мог
// записать миниатюру уже после чтения записи.
func TestDeletePublishesEveryKey(t *testing.T) {
	t.Parallel()

	processed := storedAvatar()

	pending := storedAvatar()
	pending.ThumbnailKeys = nil
	pending.ProcessingStatus = domain.ProcessingStatusPending

	tests := []struct {
		name   string
		avatar domain.Avatar
	}{
		{name: "original and thumbnails", avatar: processed},
		{name: "processing not finished", avatar: pending},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			d := newDeps()
			d.repo.avatar = tc.avatar
			svc := newService(t, d)

			require.NoError(t, svc.Delete(t.Context(), tc.avatar.ID, testUserID))
			require.Len(t, d.publisher.events, 1)

			event, ok := d.publisher.events[0].(broker.AvatarDeleteEvent)
			require.True(t, ok, "событие удаления")

			wantKeys := []string{
				tc.avatar.S3Key,
				domain.ThumbnailKey(tc.avatar.ID, domain.ThumbnailSmall),
				domain.ThumbnailKey(tc.avatar.ID, domain.ThumbnailMedium),
			}

			assert.Equal(t, tc.avatar.ID.String(), event.AvatarID)
			assert.Equal(t, wantKeys, event.S3Keys)
			assert.NotEmpty(t, event.MessageID)
		})
	}
}

func TestDeleteForbidden(t *testing.T) {
	t.Parallel()

	d := newDeps()
	d.repo.avatar = storedAvatar()
	svc := newService(t, d)

	err := svc.Delete(t.Context(), d.repo.avatar.ID, "someone-else")
	require.ErrorIs(t, err, domain.ErrForbidden)

	assert.Empty(t, d.repo.deleted, "чужой аватар остаётся на месте")
	assert.Empty(t, d.publisher.events)
}

func TestDeleteCurrentForbidden(t *testing.T) {
	t.Parallel()

	d := newDeps()
	d.repo.avatar = storedAvatar()
	svc := newService(t, d)

	err := svc.DeleteCurrent(t.Context(), testUserID, "someone-else")
	require.ErrorIs(t, err, domain.ErrForbidden)

	assert.Empty(t, d.repo.deleted)
}

func TestDeleteNotFound(t *testing.T) {
	t.Parallel()

	d := newDeps()
	d.repo.getErr = domain.ErrNotFound
	svc := newService(t, d)

	require.ErrorIs(t, svc.Delete(t.Context(), uuid.New(), testUserID), domain.ErrNotFound)
	require.ErrorIs(t, svc.DeleteCurrent(t.Context(), testUserID, testUserID), domain.ErrNotFound)

	assert.Empty(t, d.publisher.events)
}

func TestDeleteRepositoryFails(t *testing.T) {
	t.Parallel()

	d := newDeps()
	d.repo.avatar = storedAvatar()
	d.repo.deleteErr = errors.New("database is unavailable")
	svc := newService(t, d)

	require.ErrorIs(t, svc.Delete(t.Context(), d.repo.avatar.ID, testUserID), d.repo.deleteErr)

	assert.Empty(t, d.publisher.events, "уборка файлов живого аватара не заказывается")
}

// При отказе публикации файлы удаляются синхронно: переопубликовать событие
// удаления некому.
func TestDeletePublishFails(t *testing.T) {
	t.Parallel()

	d := newDeps()
	d.repo.avatar = storedAvatar()
	d.publisher.err = errors.New("broker is unavailable")
	svc := newService(t, d)

	require.NoError(t, svc.Delete(t.Context(), d.repo.avatar.ID, testUserID))

	wantKeys := []string{
		d.repo.avatar.S3Key,
		domain.ThumbnailKey(d.repo.avatar.ID, domain.ThumbnailSmall),
		domain.ThumbnailKey(d.repo.avatar.ID, domain.ThumbnailMedium),
	}
	assert.Equal(t, wantKeys, d.storage.deletedKeys)
}

// Брокер и хранилище недоступны разом: ошибка доходит до вызывающего.
func TestDeletePublishAndStorageFail(t *testing.T) {
	t.Parallel()

	d := newDeps()
	d.repo.avatar = storedAvatar()
	d.publisher.err = errors.New("broker is unavailable")
	d.storage.deleteManyErr = errors.New("storage is unavailable")
	svc := newService(t, d)

	require.ErrorIs(t, svc.Delete(t.Context(), d.repo.avatar.ID, testUserID), d.storage.deleteManyErr)
}

// Событие уборки публикуется и на отменённом контексте запроса.
func TestDeleteSurvivesCanceledRequest(t *testing.T) {
	t.Parallel()

	d := newDeps()
	d.repo.avatar = storedAvatar()
	svc := newService(t, d)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	require.NoError(t, svc.Delete(ctx, d.repo.avatar.ID, testUserID))

	assert.Equal(t, []uuid.UUID{d.repo.avatar.ID}, d.repo.deleted)
	assert.Len(t, d.publisher.events, 1)
	assert.NoError(t, d.publisher.ctxErr, "публикация идёт на неотменяемом контексте")
}

// Чужой и несуществующий аватары — отказы и для метрик тоже.
func TestDeleteReportsMetrics(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		prepare func(d *deps)
		want    []bool
	}{
		{name: "success", prepare: func(*deps) {}, want: []bool{true}},
		{name: "foreign avatar", prepare: func(d *deps) {
			d.repo.avatar.UserID = "someone-else"
		}, want: []bool{false}},
		{name: "not found", prepare: func(d *deps) {
			d.repo.getErr = domain.ErrNotFound
		}, want: []bool{false}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			d := newDeps()
			d.repo.avatar = storedAvatar()
			tc.prepare(d)

			_ = newService(t, d).Delete(t.Context(), d.repo.avatar.ID, testUserID)

			assert.Equal(t, tc.want, d.metrics.deleteOutcomes())
		})
	}
}
