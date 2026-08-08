// Package web — страницы GophProfile: форма загрузки аватара и галерея
// пользователя.
//
// Шаблоны и статика встроены в бинарник. Маршруты пакет не регистрирует:
// наружу он отдаёт хендлеры и пути, по которым их монтирует HTTP-слой.
package web

import (
	"bytes"
	"context"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/kerpe-l/gophprofile/internal/domain"
	"github.com/kerpe-l/gophprofile/internal/server/service"
)

// Пути веб-интерфейса. Их подставляют шаблоны и редирект после загрузки,
// по ним же HTTP-слой строит таблицу маршрутов.
const (
	Prefix      = "/web"
	UploadPath  = Prefix + "/upload"
	GalleryPath = Prefix + "/gallery"
	StaticPath  = Prefix + "/static"

	// GalleryPattern — маршрут галереи с параметром пути.
	GalleryPattern = GalleryPath + "/{" + paramUserID + "}"
)

// Имена полей формы и query-параметров.
const (
	// paramUserID — владелец и в пути галереи, и в форме загрузки.
	paramUserID = "user_id"
	fieldFile   = "file"
	// paramSize — размер изображения в ссылках на REST API.
	paramSize = "size"
)

// apiAvatarsPath — путь раздачи изображений. Своего канала для картинок
// у веб-интерфейса нет, страницы ссылаются на REST API.
const apiAvatarsPath = "/api/v1/avatars"

// staticMaxAge — время жизни статики в кешах: имена файлов не содержат
// хеша содержимого, поэтому кеш короткий.
const staticMaxAge = 5 * time.Minute

// Имена шаблонов и каталогов встроенных ресурсов.
const (
	baseTemplate = "base.html"
	templatesDir = "templates"
	staticDir    = "static"
)

// Заголовки и типы ответов.
const (
	headerContentType  = "Content-Type"
	headerCacheControl = "Cache-Control"
	contentTypeHTML    = "text/html; charset=utf-8"
)

// messageInternal — текст отказа, за которым скрываются внутренние детали.
const messageInternal = "Something went wrong, please try again later."

//go:embed templates
var templatesFS embed.FS

//go:embed static
var staticFS embed.FS

// Service — сценарии, которыми пользуются страницы.
type Service interface {
	// Upload сохраняет оригинал и ставит аватар в очередь на обработку.
	Upload(ctx context.Context, in service.UploadInput) (domain.Avatar, error)
	// ListByUser возвращает живые аватары пользователя от новых к старым.
	ListByUser(ctx context.Context, userID string) ([]domain.Avatar, error)
}

// Deps — зависимости веб-интерфейса.
type Deps struct {
	Service Service
	// MaxUploadBytes — предел размера файла; форма показывает его пользователю.
	MaxUploadBytes int64
	Log            *slog.Logger
}

// Handlers — хендлеры страниц вместе с разобранными шаблонами.
type Handlers struct {
	svc       Service
	maxUpload int64
	pages     pages
	static    http.Handler
	log       *slog.Logger
}

// pages — шаблоны страниц, разобранные при старте.
type pages struct {
	upload  *template.Template
	gallery *template.Template
	failure *template.Template
}

// New разбирает шаблоны и готовит раздачу статики. Ошибка означает, что
// встроенные ресурсы не собираются, — приложение с такой сборкой не стартует.
func New(deps Deps) (*Handlers, error) {
	upload, err := parsePage("upload.html")
	if err != nil {
		return nil, err
	}

	gallery, err := parsePage("gallery.html")
	if err != nil {
		return nil, err
	}

	failure, err := parsePage("error.html")
	if err != nil {
		return nil, err
	}

	assets, err := fs.Sub(staticFS, staticDir)
	if err != nil {
		return nil, fmt.Errorf("open static assets: %w", err)
	}

	return &Handlers{
		svc:       deps.Service,
		maxUpload: deps.MaxUploadBytes,
		pages:     pages{upload: upload, gallery: gallery, failure: failure},
		static:    cacheControl(http.FileServerFS(assets), staticMaxAge),
		log:       deps.Log,
	}, nil
}

// Static отдаёт встроенные css и js.
func (h *Handlers) Static() http.Handler {
	return h.static
}

// parsePage собирает страницу из каркаса и её собственных блоков.
func parsePage(name string) (*template.Template, error) {
	page, err := template.New(baseTemplate).Funcs(funcs()).ParseFS(templatesFS,
		templatesDir+"/"+baseTemplate, templatesDir+"/"+name)
	if err != nil {
		return nil, fmt.Errorf("parse template %s: %w", name, err)
	}

	return page, nil
}

// funcs даёт шаблонам адреса страниц, чтобы маршруты не расползались
// по разметке строковыми литералами.
func funcs() template.FuncMap {
	return template.FuncMap{
		"uploadURL":  uploadURL,
		"galleryURL": GalleryURL,
		"staticURL":  staticURL,
	}
}

// GalleryURL — адрес галереи пользователя.
func GalleryURL(userID string) string {
	return GalleryPath + "/" + url.PathEscape(userID)
}

// uploadURL — адрес формы загрузки; непустой владелец подставляется в поле.
func uploadURL(userID string) string {
	if userID == "" {
		return UploadPath
	}

	return UploadPath + "?" + url.Values{paramUserID: {userID}}.Encode()
}

// staticURL — адрес встроенного ресурса.
func staticURL(name string) string {
	return StaticPath + "/" + name
}

// render отдаёт страницу. Шаблон разворачивается в буфер: отказ на середине
// иначе дал бы 200 с половиной разметки.
func (h *Handlers) render(
	w http.ResponseWriter, r *http.Request, status int, page *template.Template, data any,
) {
	var buf bytes.Buffer

	if err := page.ExecuteTemplate(&buf, baseTemplate, data); err != nil {
		h.log.ErrorContext(r.Context(), "render page", slog.Any("error", err))
		http.Error(w, messageInternal, http.StatusInternalServerError)

		return
	}

	w.Header().Set(headerContentType, contentTypeHTML)
	w.WriteHeader(status)

	if _, err := buf.WriteTo(w); err != nil {
		// Ответ уже начат, сообщить об обрыве нечем. Обычная причина —
		// ушедший клиент.
		h.log.WarnContext(r.Context(), "write page", slog.Any("error", err))
	}
}

// renderFailure показывает страницу отказа.
func (h *Handlers) renderFailure(w http.ResponseWriter, r *http.Request, err error) {
	status, message := h.describeError(err)
	h.logFailure(r, status, err)

	h.render(w, r, status, h.pages.failure, errorView{Status: status, Message: message})
}

// describeError переводит ошибку в HTTP-статус и текст для страницы.
// Наружу уходит общая формулировка: внутренняя ошибка может содержать пути,
// имена таблиц и адреса.
func (h *Handlers) describeError(err error) (int, string) {
	switch {
	case errors.Is(err, domain.ErrTooLarge):
		return http.StatusRequestEntityTooLarge, "File is larger than " + humanSize(h.maxUpload) + "."

	case errors.Is(err, domain.ErrUnsupportedFormat):
		return http.StatusBadRequest, "Unsupported file format. Upload a JPEG, PNG or WebP image."

	case errors.Is(err, domain.ErrImageTooBig):
		return http.StatusBadRequest, "Image has too many pixels."

	case errors.Is(err, errMissingUserID):
		return http.StatusBadRequest, "Enter the user id."

	case errors.Is(err, errUserIDTooLong):
		return http.StatusBadRequest, "User id must be at most " + strconv.Itoa(maxUserIDLen) + " characters."

	case errors.Is(err, errMissingFile):
		return http.StatusBadRequest, "Choose an image to upload."

	case errors.Is(err, errFileNameTooLong):
		return http.StatusBadRequest, "File name must be at most " + strconv.Itoa(maxFileNameLen) + " characters."

	case errors.Is(err, errMalformedBody):
		return http.StatusBadRequest, "The form could not be read."

	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout, "The request took too long."

	default:
		return http.StatusInternalServerError, messageInternal
	}
}

// logFailure пишет отказ страницы: наружу ушёл только общий текст.
func (h *Handlers) logFailure(r *http.Request, status int, err error) {
	level := slog.LevelWarn
	if status >= http.StatusInternalServerError {
		level = slog.LevelError
	}

	h.log.Log(r.Context(), level, "web request failed",
		slog.Any("error", err),
		slog.String("method", r.Method),
		slog.String("path", r.URL.Path),
		slog.Int("status", status),
	)
}

// cacheControl проставляет время жизни ответа в кешах.
func cacheControl(next http.Handler, maxAge time.Duration) http.Handler {
	value := "max-age=" + strconv.Itoa(int(maxAge.Seconds()))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(headerCacheControl, value)

		next.ServeHTTP(w, r)
	})
}
