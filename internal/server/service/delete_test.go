package service_test

import (
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

// TestDeletePublishesEveryKey — файлы убирает воркер по этому событию, и всё,
// чего в нём нет, останется в хранилище навсегда.
func TestDeletePublishesEveryKey(t *testing.T) {
	t.Parallel()

	processed := storedAvatar()

	pending := storedAvatar()
	pending.ThumbnailKeys = nil
	pending.ProcessingStatus = domain.ProcessingStatusPending

	tests := []struct {
		name     string
		avatar   domain.Avatar
		wantKeys []string
	}{
		{
			name:   "original and thumbnails",
			avatar: processed,
			wantKeys: []string{
				processed.S3Key,
				domain.ThumbnailKey(processed.ID, domain.ThumbnailSmall),
				domain.ThumbnailKey(processed.ID, domain.ThumbnailMedium),
			},
		},
		{
			name:     "original only",
			avatar:   pending,
			wantKeys: []string{pending.S3Key},
		},
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

			assert.Equal(t, tc.avatar.ID.String(), event.AvatarID)
			assert.Equal(t, tc.wantKeys, event.S3Keys)
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

// TestDeleteNotFound — удаление идемпотентно: повторный запрос не находит
// живой записи, потому что мягко удалённые из выборок исключены.
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

// TestDeletePublishFails — в отличие от загрузки, отказ публикации здесь
// доходит до вызывающего: переопубликовать событие удаления некому,
// и без него файлы остались бы в хранилище молча.
func TestDeletePublishFails(t *testing.T) {
	t.Parallel()

	d := newDeps()
	d.repo.avatar = storedAvatar()
	d.publisher.err = errors.New("broker is unavailable")
	svc := newService(t, d)

	require.ErrorIs(t, svc.Delete(t.Context(), d.repo.avatar.ID, testUserID), d.publisher.err)
}
