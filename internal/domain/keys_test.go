package domain_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kerpe-l/gophprofile/internal/domain"
)

// avatarID — фиксированный идентификатор: ключи должны совпадать байт в байт,
// иначе загруженный оригинал не найдётся при раздаче.
const avatarID = "7c9e6679-7425-40de-944b-e07fc1f90ae7"

func TestOriginalKey(t *testing.T) {
	t.Parallel()

	id, err := uuid.Parse(avatarID)
	require.NoError(t, err)

	assert.Equal(t, "originals/"+avatarID, domain.OriginalKey(id))
}

func TestThumbnailKey(t *testing.T) {
	t.Parallel()

	id, err := uuid.Parse(avatarID)
	require.NoError(t, err)

	tests := []struct {
		name string
		size domain.ThumbnailSize
		want string
	}{
		{name: "small", size: domain.ThumbnailSmall, want: "thumbnails/" + avatarID + "/100x100"},
		{name: "medium", size: domain.ThumbnailMedium, want: "thumbnails/" + avatarID + "/300x300"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, domain.ThumbnailKey(id, tc.size))
		})
	}
}

// Ключ оригинала и ключи миниатюр одного аватара не должны совпадать:
// иначе миниатюра затрёт оригинал в хранилище.
func TestKeysDoNotCollide(t *testing.T) {
	t.Parallel()

	id := uuid.New()

	keys := map[string]struct{}{domain.OriginalKey(id): {}}

	for _, size := range domain.ThumbnailSizes() {
		key := domain.ThumbnailKey(id, size)
		_, seen := keys[key]
		assert.False(t, seen, "key %s is not unique", key)
		keys[key] = struct{}{}
	}
}
