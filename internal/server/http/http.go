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
	"github.com/kerpe-l/gophprofile/internal/metrics"
	"github.com/kerpe-l/gophprofile/internal/server/service"
	"github.com/kerpe-l/gophprofile/internal/server/web"
)

// Префикс REST API и служебные пути.
const (
	apiPrefix   = "/api/v1"
	healthPath  = "/health"
	metricsPath = "/metrics"
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
	// Web — страницы веб-интерфейса; без них раздел /web не монтируется.
	Web *web.Handlers
	// Tracing включает серверный спан на каждый запрос.
	Tracing bool
	// Metrics включает RED-метрики на каждый запрос.
	Metrics *metrics.HTTP
	// MetricsHandler — выдача метрик; без него /metrics не монтируется.
	MetricsHandler http.Handler
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

	// Спан открывается раньше остальных middleware, чтобы запись лога о
	// запросе уже несла trace_id.
	if deps.Tracing {
		r.Use(tracing)
	}

	if deps.Metrics != nil {
		r.Use(measuring(deps.Metrics))
	}

	r.Use(requestID, logging(deps.Log), recovering(deps.Log))

	r.Get(healthPath, a.health)

	if deps.MetricsHandler != nil {
		r.Method(http.MethodGet, metricsPath, deps.MetricsHandler)
	}

	r.Route(apiPrefix, func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(uploading(deps)...)
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

	if deps.Web != nil {
		mountWeb(r, deps)
	}

	return r
}

// uploading — ограничитель тела и сроки для приёма файла. Предел тела равен
// размеру файла плюс запас на обвязку multipart; сам файл сверяется с пределом
// в хендлере.
func uploading(deps Deps) []func(http.Handler) http.Handler {
	return []func(http.Handler) http.Handler{
		maxBytes(deps.MaxUploadBytes + multipartOverheadBytes),
		timeout(deps.HTTP.ReadTimeout),
		extendWriteDeadline(deps.HTTP.ReadTimeout+deps.HTTP.WriteTimeout, deps.Log),
	}
}

// mountWeb добавляет страницы веб-интерфейса. Форма загрузки получает те же
// сроки и ограничитель тела, что и загрузка в API.
func mountWeb(r chi.Router, deps Deps) {
	r.Group(func(r chi.Router) {
		r.Use(timeout(deps.HTTP.RequestTimeout))

		r.Get(web.UploadPath, deps.Web.UploadForm)
		r.Get(web.GalleryPattern, deps.Web.Gallery)
		r.Mount(web.StaticPath, http.StripPrefix(web.StaticPath, deps.Web.Static()))
	})

	r.Group(func(r chi.Router) {
		r.Use(uploading(deps)...)

		r.Post(web.UploadPath, deps.Web.Upload)
	})
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

func thumbnailSize(r *http.Request) (domain.ThumbnailSize, error) {
	return domain.ParseThumbnailSize(r.URL.Query().Get(paramSize))
}
