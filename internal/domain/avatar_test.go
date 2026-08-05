package domain_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/kerpe-l/gophprofile/internal/domain"
)

func TestAvatarThumbnail(t *testing.T) {
	t.Parallel()

	keys := map[domain.ThumbnailSize]string{
		domain.ThumbnailSmall: "thumbnails/id/100x100",
	}

	tests := []struct {
		name    string
		avatar  domain.Avatar
		size    domain.ThumbnailSize
		wantKey string
		wantOK  bool
	}{
		{
			name:    "ready thumbnail",
			avatar:  domain.Avatar{ProcessingStatus: domain.ProcessingStatusCompleted, ThumbnailKeys: keys},
			size:    domain.ThumbnailSmall,
			wantKey: "thumbnails/id/100x100",
			wantOK:  true,
		},
		{
			name:   "size is missing from a completed set",
			avatar: domain.Avatar{ProcessingStatus: domain.ProcessingStatusCompleted, ThumbnailKeys: keys},
			size:   domain.ThumbnailMedium,
		},
		{
			// Ключи могли остаться от предыдущей обработки, но пока она не
			// завершена, отдавать их нельзя: они могут указывать на уже удалённые объекты.
			name:   "processing is not finished",
			avatar: domain.Avatar{ProcessingStatus: domain.ProcessingStatusProcessing, ThumbnailKeys: keys},
			size:   domain.ThumbnailSmall,
		},
		{
			name:   "processing failed",
			avatar: domain.Avatar{ProcessingStatus: domain.ProcessingStatusFailed, ThumbnailKeys: keys},
			size:   domain.ThumbnailSmall,
		},
		{
			name:   "no thumbnails at all",
			avatar: domain.Avatar{ProcessingStatus: domain.ProcessingStatusCompleted},
			size:   domain.ThumbnailSmall,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			key, ok := tc.avatar.Thumbnail(tc.size)

			assert.Equal(t, tc.wantOK, ok)
			assert.Equal(t, tc.wantKey, key)
		})
	}
}
