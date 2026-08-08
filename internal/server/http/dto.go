package http

import (
	"time"

	"github.com/google/uuid"

	"github.com/kerpe-l/gophprofile/internal/domain"
)

type uploadResponse struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	URL       string    `json:"url"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

type metadataResponse struct {
	ID         string         `json:"id"`
	UserID     string         `json:"user_id"`
	FileName   string         `json:"file_name"`
	MimeType   string         `json:"mime_type"`
	Size       int64          `json:"size"`
	Dimensions dimensions     `json:"dimensions"`
	Thumbnails []thumbnailRef `json:"thumbnails"`
	CreatedAt  time.Time      `json:"created_at"`
	UpdatedAt  time.Time      `json:"updated_at"`
}

// dimensions — размеры оригинала в пикселях.
type dimensions struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

// thumbnailRef — ссылка на миниатюру одного размера.
type thumbnailRef struct {
	Size string `json:"size"`
	URL  string `json:"url"`
}

type listResponse struct {
	Avatars []metadataResponse `json:"avatars"`
}

func newUploadResponse(avatar domain.Avatar) uploadResponse {
	return uploadResponse{
		ID:        avatar.ID.String(),
		UserID:    avatar.UserID,
		URL:       avatarURL(avatar.ID),
		Status:    apiStatus(avatar.ProcessingStatus),
		CreatedAt: avatar.CreatedAt,
	}
}

// newMetadataResponse собирает метаданные аватара.
//
// Перечисляются только готовые миниатюры: ссылка на несозданную обещала бы
// размер, которого клиент не получит — по ней отдаётся оригинал.
func newMetadataResponse(avatar domain.Avatar) metadataResponse {
	sizes := domain.ThumbnailSizes()
	thumbnails := make([]thumbnailRef, 0, len(sizes))

	for _, size := range sizes {
		if _, ok := avatar.Thumbnail(size); !ok {
			continue
		}

		thumbnails = append(thumbnails, thumbnailRef{
			Size: string(size),
			URL:  thumbnailURL(avatar.ID, size),
		})
	}

	return metadataResponse{
		ID:         avatar.ID.String(),
		UserID:     avatar.UserID,
		FileName:   avatar.FileName,
		MimeType:   avatar.MimeType,
		Size:       avatar.SizeBytes,
		Dimensions: dimensions{Width: avatar.Width, Height: avatar.Height},
		Thumbnails: thumbnails,
		CreatedAt:  avatar.CreatedAt,
		UpdatedAt:  avatar.UpdatedAt,
	}
}

// apiStatus переводит состояние обработки в статус для клиента: снаружи
// видно только, готовы миниатюры или ещё нет.
func apiStatus(status domain.ProcessingStatus) string {
	switch status {
	case domain.ProcessingStatusCompleted:
		return "completed"
	case domain.ProcessingStatusFailed:
		return "failed"
	case domain.ProcessingStatusPending, domain.ProcessingStatusProcessing:
		return "processing"
	default:
		return "processing"
	}
}

// avatarURL — путь к изображению аватара.
//
// Ссылки относительные: за прокси сервис не знает своего внешнего адреса,
// а собранный по заголовку Host абсолютный URL ломается при смене домена.
func avatarURL(id uuid.UUID) string {
	return apiPrefix + "/avatars/" + id.String()
}

// thumbnailURL — путь к миниатюре указанного размера.
func thumbnailURL(id uuid.UUID, size domain.ThumbnailSize) string {
	return avatarURL(id) + "?" + paramSize + "=" + string(size)
}
