package web

import (
	"strconv"

	"github.com/google/uuid"

	"github.com/kerpe-l/gophprofile/internal/domain"
)

// timeLayout — как даты выглядят на страницах.
const timeLayout = "2006-01-02 15:04"

// Единицы размера файла.
const (
	kilobyte = 1 << 10
	megabyte = 1 << 20
)

// uploadView — форма загрузки. Error непуст, когда форма показана в ответ
// на неудачную отправку.
type uploadView struct {
	UserID  string
	Error   string
	MaxSize string
}

type galleryView struct {
	UserID  string
	Avatars []avatarView
}

// avatarView — карточка аватара в галерее.
type avatarView struct {
	FileName     string
	ThumbnailURL string
	OriginalURL  string
	Size         string
	Dimensions   string
	CreatedAt    string
	Status       string
	// IsCurrent — аватар, который сервис отдаёт как актуальный.
	IsCurrent bool
}

type errorView struct {
	Status  int
	Message string
}

func (h *Handlers) newUploadView(userID, failure string) uploadView {
	return uploadView{
		UserID:  userID,
		Error:   failure,
		MaxSize: humanSize(h.maxUpload),
	}
}

// newGalleryView собирает галерею. Список приходит от новых к старым,
// поэтому актуальный аватар — первый.
func newGalleryView(userID string, avatars []domain.Avatar) galleryView {
	items := make([]avatarView, 0, len(avatars))

	for i, avatar := range avatars {
		items = append(items, avatarView{
			FileName:     avatar.FileName,
			ThumbnailURL: thumbnailURL(avatar.ID, domain.ThumbnailSmall),
			OriginalURL:  avatarURL(avatar.ID),
			Size:         humanSize(avatar.SizeBytes),
			Dimensions:   strconv.Itoa(avatar.Width) + "×" + strconv.Itoa(avatar.Height),
			CreatedAt:    avatar.CreatedAt.Format(timeLayout),
			Status:       statusText(avatar.ProcessingStatus),
			IsCurrent:    i == 0,
		})
	}

	return galleryView{UserID: userID, Avatars: items}
}

// avatarURL — путь к оригиналу аватара.
func avatarURL(id uuid.UUID) string {
	return apiAvatarsPath + "/" + id.String()
}

// thumbnailURL — путь к миниатюре. Ссылка ставится независимо от готовности:
// до появления миниатюры по ней отдаётся оригинал.
func thumbnailURL(id uuid.UUID, size domain.ThumbnailSize) string {
	return avatarURL(id) + "?" + paramSize + "=" + string(size)
}

// statusText описывает состояние обработки для читателя страницы.
func statusText(status domain.ProcessingStatus) string {
	switch status {
	case domain.ProcessingStatusCompleted:
		return "thumbnails ready"
	case domain.ProcessingStatusFailed:
		return "processing failed"
	case domain.ProcessingStatusPending, domain.ProcessingStatusProcessing:
		return "processing"
	default:
		return "processing"
	}
}

// humanSize округляет размер до килобайт или мегабайт.
func humanSize(size int64) string {
	switch {
	case size >= megabyte:
		return strconv.FormatFloat(float64(size)/megabyte, 'f', 1, 64) + " MB"
	case size >= kilobyte:
		return strconv.FormatFloat(float64(size)/kilobyte, 'f', 1, 64) + " KB"
	default:
		return strconv.FormatInt(size, 10) + " B"
	}
}
