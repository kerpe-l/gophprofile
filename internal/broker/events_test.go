package broker_test

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kerpe-l/gophprofile/internal/broker"
	"github.com/kerpe-l/gophprofile/internal/domain"
)

// avatarID — идентификатор аватара в событиях тестов.
const avatarID = "97b3d1c8-0000-4000-8000-000000000000"

// Формат события фиксирован: обработчик и публикатор живут в разных
// бинарниках и разъезжаются молча.
func TestEventJSON(t *testing.T) {
	t.Parallel()

	id := uuid.MustParse(avatarID)

	tests := []struct {
		name  string
		event broker.Event
		want  string
	}{
		{
			name:  "upload",
			event: broker.AvatarUploadEvent{MessageID: "m-1", AvatarID: avatarID, UserID: "u-1", S3Key: "originals/" + avatarID},
			want:  `{"message_id":"m-1","avatar_id":"` + avatarID + `","user_id":"u-1","s3_key":"originals/` + avatarID + `"}`,
		},
		{
			name:  "delete",
			event: broker.AvatarDeleteEvent{MessageID: "m-2", AvatarID: avatarID, S3Keys: []string{"originals/" + avatarID}},
			want:  `{"message_id":"m-2","avatar_id":"` + avatarID + `","s3_keys":["originals/` + avatarID + `"]}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			body, err := json.Marshal(tc.event)
			require.NoError(t, err)
			assert.JSONEq(t, tc.want, string(body))
		})
	}

	assert.Equal(t, broker.RoutingKeyUploaded, broker.NewUploadEvent(id, "u-1", "").RoutingKey())
	assert.Equal(t, broker.RoutingKeyDeleted, broker.NewDeleteEvent(id, nil).RoutingKey())
}

func TestNewEventGeneratesMessageID(t *testing.T) {
	t.Parallel()

	id := uuid.MustParse(avatarID)
	key := domain.OriginalKey(id)

	upload := broker.NewUploadEvent(id, "u-1", key)
	assert.Equal(t, avatarID, upload.AvatarID)
	assert.Equal(t, key, upload.S3Key)

	deleted := broker.NewDeleteEvent(id, []string{key})
	assert.Equal(t, avatarID, deleted.AvatarID)

	// Идентификаторы сообщений различают повторные публикации одного
	// и того же события в логах.
	assert.NotEqual(t, upload.ID(), deleted.ID())
	assert.NotEqual(t, upload.ID(), broker.NewUploadEvent(id, "u-1", key).ID())
}
