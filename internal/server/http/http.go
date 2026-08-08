// Package http — REST API аватарного сервиса: роутер, middleware и хендлеры.
package http

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/kerpe-l/gophprofile/internal/config"
	"github.com/kerpe-l/gophprofile/internal/domain"
	"github.com/kerpe-l/gophprofile/internal/server/service"
)

// Префикс REST API и путь проверки состояния.
const (
	apiPrefix  = "/api/v1"
	healthPath = "/health"
)

// Имена компонентов в ответе /health.
const (
	ComponentDB     = "db"
	ComponentS3     = "s3"
	ComponentBroker = "broker"
)

// Service — сценарии работы с аватарами.
type Service interface {
	// Upload сохраняет оригинал и ставит аватар в очередь на обработку.
	Upload(ctx context.Context, in service.UploadInput) (domain.Avatar, error)
	// AvatarContent отдаёт изображение конкретного аватара; заглушки нет.
	AvatarContent(ctx context.Context, id uuid.UUID, size domain.ThumbnailSize) (service.Content, error)
	// UserAvatarContent отдаёт актуальный аватар пользователя либо заглушку.
	UserAvatarContent(ctx context.Context, userID string, size domain.ThumbnailSize) (service.Content, error)
	// Metadata возвращает метаданные аватара.
	Metadata(ctx context.Context, id uuid.UUID) (domain.Avatar, error)
	// ListByUser возвращает живые аватары пользователя от новых к старым.
	ListByUser(ctx context.Context, userID string) ([]domain.Avatar, error)
	// Delete удаляет аватар по идентификатору от имени requesterID.
	Delete(ctx context.Context, id uuid.UUID, requesterID string) error
	// DeleteCurrent удаляет актуальный аватар пользователя.
	DeleteCurrent(ctx context.Context, userID, requesterID string) error
}

// Checker — зависимость, доступность которой показывает /health.
type Checker interface {
	// Ping возвращает ошибку, если зависимость недоступна.
	Ping(ctx context.Context) error
}

// Deps — зависимости HTTP-слоя.
type Deps struct {
	Service Service
	// Checks — проверяемые в /health зависимости по именам компонентов.
	Checks map[string]Checker
	HTTP   config.HTTP
	// MaxUploadBytes — предельный размер самого файла, без обвязки multipart.
	MaxUploadBytes int64
	Log            *slog.Logger
}

// api — состояние хендлеров.
type api struct {
	svc       Service
	checks    map[string]Checker
	maxUpload int64
	log       *slog.Logger
}

// New собирает роутер сервиса. Загрузка идёт отдельной группой: у неё свой
// предел времени на заливку и ограничитель размера тела.
func New(deps Deps) *chi.Mux {
	a := &api{
		svc:       deps.Service,
		checks:    deps.Checks,
		maxUpload: deps.MaxUploadBytes,
		log:       deps.Log,
	}

	r := chi.NewRouter()
	r.Use(requestID, logging(deps.Log), recovering(deps.Log))

	r.Get(healthPath, a.health)

	r.Route(apiPrefix, func(r chi.Router) {
		r.Group(func(r chi.Router) {
			// Предел тела — размер файла плюс запас на обвязку multipart;
			// сам файл сверяется с пределом в хендлере.
			r.Use(
				maxBytes(deps.MaxUploadBytes+multipartOverheadBytes),
				timeout(deps.HTTP.ReadTimeout),
				extendWriteDeadline(deps.HTTP.ReadTimeout+deps.HTTP.WriteTimeout, deps.Log),
			)
			r.Post("/avatars", a.upload)
		})

		r.Group(func(r chi.Router) {
			r.Use(timeout(deps.HTTP.RequestTimeout))

			r.Get("/avatars/{"+paramAvatarID+"}", a.avatarContent)
			r.Get("/avatars/{"+paramAvatarID+"}/metadata", a.avatarMetadata)
			r.Delete("/avatars/{"+paramAvatarID+"}", a.deleteAvatar)

			r.Get("/users/{"+paramUserID+"}/avatar", a.userAvatarContent)
			r.Get("/users/{"+paramUserID+"}/avatars", a.userAvatars)
			r.Delete("/users/{"+paramUserID+"}/avatar", a.deleteUserAvatar)
		})
	})

	return r
}

// requesterID возвращает владельца, от имени которого пришёл запрос.
// Заголовок проставляет доверенный gateway; аутентификацией сервис
// не занимается и лишь сверяет владение при удалении.
func requesterID(r *http.Request) (string, error) {
	id := r.Header.Get(headerUserID)
	if id == "" {
		return "", errMissingUserID
	}

	if len(id) > maxUserIDLen {
		return "", errUserIDTooLong
	}

	return id, nil
}

// avatarID разбирает идентификатор аватара из пути.
func avatarID(r *http.Request) (uuid.UUID, error) {
	id, err := uuid.Parse(chi.URLParam(r, paramAvatarID))
	if err != nil {
		return uuid.Nil, errInvalidAvatarID
	}

	return id, nil
}

// thumbnailSize разбирает запрошенный размер изображения.
func thumbnailSize(r *http.Request) (domain.ThumbnailSize, error) {
	return domain.ParseThumbnailSize(r.URL.Query().Get(paramSize))
}
