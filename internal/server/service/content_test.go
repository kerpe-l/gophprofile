package service_test

import (
	"errors"
	"io"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kerpe-l/gophprofile/internal/domain"
	"github.com/kerpe-l/gophprofile/internal/placeholder"
)

// storedAvatar — аватар с завершённой обработкой и готовыми миниатюрами.
func storedAvatar() domain.Avatar {
	id := uuid.New()

	return domain.Avatar{
		ID:               id,
		UserID:           testUserID,
		MimeType:         "image/png",
		S3Key:            domain.OriginalKey(id),
		ThumbnailKeys:    thumbnailKeys(id),
		UploadStatus:     domain.UploadStatusUploaded,
		ProcessingStatus: domain.ProcessingStatusCompleted,
	}
}

func thumbnailKeys(id uuid.UUID) map[domain.ThumbnailSize]string {
	keys := make(map[domain.ThumbnailSize]string, len(domain.ThumbnailSizes()))
	for _, size := range domain.ThumbnailSizes() {
		keys[size] = domain.ThumbnailKey(id, size)
	}

	return keys
}

func TestAvatarContent(t *testing.T) {
	t.Parallel()

	ready := storedAvatar()

	pending := storedAvatar()
	pending.ThumbnailKeys = nil
	pending.ProcessingStatus = domain.ProcessingStatusPending

	failed := storedAvatar()
	failed.ThumbnailKeys = nil
	failed.ProcessingStatus = domain.ProcessingStatusFailed

	tests := []struct {
		name       string
		avatar     domain.Avatar
		size       domain.ThumbnailSize
		wantKey    string
		wantMaxAge time.Duration
	}{
		{
			name:       "original",
			avatar:     ready,
			size:       "",
			wantKey:    ready.S3Key,
			wantMaxAge: 24 * time.Hour,
		},
		{
			name:       "ready thumbnail",
			avatar:     ready,
			size:       domain.ThumbnailSmall,
			wantKey:    domain.ThumbnailKey(ready.ID, domain.ThumbnailSmall),
			wantMaxAge: 24 * time.Hour,
		},
		{
			// Пока миниатюры нет, отдаётся оригинал — и с коротким кешем,
			// иначе клиент не увидит готовую миниатюру до конца суток.
			name:       "thumbnail is not ready yet",
			avatar:     pending,
			size:       domain.ThumbnailMedium,
			wantKey:    pending.S3Key,
			wantMaxAge: time.Minute,
		},
		{
			// Терминальный failed: подмена постоянна, кеш обычный.
			name:       "processing failed for good",
			avatar:     failed,
			size:       domain.ThumbnailMedium,
			wantKey:    failed.S3Key,
			wantMaxAge: 24 * time.Hour,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			d := newDeps()
			d.repo.avatar = tc.avatar
			svc := newService(t, d)

			content, err := svc.AvatarContent(t.Context(), tc.avatar.ID, tc.size)
			require.NoError(t, err)

			defer func() {
				assert.NoError(t, content.Body.Close())
			}()

			assert.Equal(t, []string{tc.wantKey}, d.storage.getKeys)
			assert.Equal(t, tc.wantMaxAge, content.MaxAge)
			assert.False(t, content.IsDefault)

			assert.Equal(t, "image/png", content.ContentType)
			assert.Equal(t, d.storage.etag, content.ETag)
			assert.Equal(t, int64(len(d.storage.body)), content.Size)

			body, err := io.ReadAll(content.Body)
			require.NoError(t, err)
			assert.Equal(t, d.storage.body, body)
		})
	}
}

// TestAvatarContentNotFound — на запрос конкретного аватара заглушка
// не подставляется: запрошен объект по идентификатору.
func TestAvatarContentNotFound(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		setup func(d *deps)
	}{
		{
			name:  "no record",
			setup: func(d *deps) { d.repo.getErr = domain.ErrNotFound },
		},
		{
			name: "no object in storage",
			setup: func(d *deps) {
				d.repo.avatar = storedAvatar()
				d.storage.getErr = domain.ErrNotFound
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			d := newDeps()
			tc.setup(d)
			svc := newService(t, d)

			_, err := svc.AvatarContent(t.Context(), uuid.New(), "")
			require.ErrorIs(t, err, domain.ErrNotFound)
		})
	}
}

func TestUserAvatarContent(t *testing.T) {
	t.Parallel()

	d := newDeps()
	d.repo.avatar = storedAvatar()
	svc := newService(t, d)

	content, err := svc.UserAvatarContent(t.Context(), testUserID, domain.ThumbnailSmall)
	require.NoError(t, err)

	defer func() {
		assert.NoError(t, content.Body.Close())
	}()

	assert.False(t, content.IsDefault)
	assert.Equal(t, []string{domain.ThumbnailKey(d.repo.avatar.ID, domain.ThumbnailSmall)}, d.storage.getKeys)
}

// TestUserAvatarContentFallsBackToPlaceholder — назначение сервиса в том
// и состоит, чтобы отдать изображение всегда: незавершённая загрузка для
// стороннего клиента не отличается от отсутствия аватара.
func TestUserAvatarContentFallsBackToPlaceholder(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		setup func(d *deps)
	}{
		{
			name:  "user has no avatar",
			setup: func(d *deps) { d.repo.getErr = domain.ErrNotFound },
		},
		{
			name: "original is not in storage yet",
			setup: func(d *deps) {
				avatar := storedAvatar()
				avatar.UploadStatus = domain.UploadStatusUploading
				d.repo.avatar = avatar
				d.storage.getErr = domain.ErrNotFound
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			d := newDeps()
			tc.setup(d)
			svc := newService(t, d)

			content, err := svc.UserAvatarContent(t.Context(), testUserID, "")
			require.NoError(t, err)

			defer func() {
				assert.NoError(t, content.Body.Close())
			}()

			ph := placeholder.New()

			assert.True(t, content.IsDefault)
			assert.Equal(t, placeholder.ContentType, content.ContentType)
			assert.Equal(t, ph.ETag(), content.ETag)
			assert.Equal(t, ph.Size(), content.Size)
			assert.Equal(t, 5*time.Minute, content.MaxAge)

			body, err := io.ReadAll(content.Body)
			require.NoError(t, err)
			assert.Len(t, body, int(ph.Size()))
		})
	}
}

// TestUserAvatarContentPropagatesFailure — заглушка подменяет отсутствующий
// аватар, а не сломанную зависимость: отказ базы обязан дойти до вызывающего.
func TestUserAvatarContentPropagatesFailure(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		setup func(d *deps)
	}{
		{
			name:  "database is unavailable",
			setup: func(d *deps) { d.repo.getErr = errors.New("database is unavailable") },
		},
		{
			name: "storage is unavailable",
			setup: func(d *deps) {
				d.repo.avatar = storedAvatar()
				d.storage.getErr = errStorage
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			d := newDeps()
			tc.setup(d)
			svc := newService(t, d)

			_, err := svc.UserAvatarContent(t.Context(), testUserID, "")
			require.Error(t, err)
			assert.NotErrorIs(t, err, domain.ErrNotFound)
		})
	}
}
