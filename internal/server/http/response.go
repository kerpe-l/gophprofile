package http

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/kerpe-l/gophprofile/internal/domain"
)

// Имена заголовков и параметров запроса.
const (
	headerRequestID     = "X-Request-ID"
	headerUserID        = "X-User-ID"
	headerAvatarDefault = "X-Avatar-Default"
	headerContentType   = "Content-Type"
	headerContentLength = "Content-Length"
	headerCacheControl  = "Cache-Control"
	headerETag          = "ETag"
	headerIfNoneMatch   = "If-None-Match"

	paramAvatarID = "avatar_id"
	paramUserID   = "user_id"
	paramSize     = "size"
)

// contentTypeJSON — тип тела ответов API.
const contentTypeJSON = "application/json; charset=utf-8"

// Тексты ответов об ошибках. Наружу уходит общая формулировка: текст
// внутренней ошибки может содержать пути, имена таблиц и адреса.
const (
	messageInternal      = "Internal error"
	messageNotFound      = "Avatar not found"
	messageForbidden     = "Forbidden"
	messageInvalidFormat = "Invalid file format"
	messageTooLarge      = "File too large"
	messageBadRequest    = "Bad request"
	messageConflict      = "Avatar state changed"
	messageTimeout       = "Request timeout"
)

// Ошибки транспорта: причины, по которым запрос не доходит до сервиса.
var (
	// errMissingUserID — не задан обязательный для записи заголовок владельца.
	errMissingUserID = errors.New("missing user id header")
	// errInvalidAvatarID — идентификатор аватара в пути не является UUID.
	errInvalidAvatarID = errors.New("invalid avatar id")
	// errMissingFile — в форме нет файла ни под одним из принимаемых имён.
	errMissingFile = errors.New("missing file field")
	// errMalformedBody — тело не разбирается как multipart/form-data.
	errMalformedBody = errors.New("malformed multipart body")
)

// errorResponse — тело ответа об ошибке.
type errorResponse struct {
	// Error — краткая формулировка для клиента.
	Error string `json:"error"`
	// Details — уточнение, если оно есть.
	Details string `json:"details,omitempty"`
	// MaxSize — предельный размер загрузки; заполняется только при отказе
	// по размеру, поэтому omitzero: ноль здесь означал бы запрет загрузки.
	MaxSize int64 `json:"max_size,omitzero"`
}

// respond переводит ошибку в ответ клиенту. Всё, что не разобрано в статус
// явно, отдаётся как внутренняя ошибка и уходит в лог целиком: клиенту
// подробности не нужны, а найти причину без них нельзя.
func (a *api) respond(w http.ResponseWriter, r *http.Request, err error) {
	status, body := a.mapError(err)

	level := slog.LevelWarn
	if status >= http.StatusInternalServerError {
		level = slog.LevelError
	}

	a.log.Log(r.Context(), level, "request failed",
		slog.Any("error", err),
		slog.String("method", r.Method),
		slog.String("path", r.URL.Path),
		slog.Int("status", status),
	)

	writeJSON(r.Context(), w, a.log, status, body)
}

// mapError переводит ошибку в HTTP-статус и безопасное для клиента тело.
func (a *api) mapError(err error) (int, errorResponse) {
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return http.StatusNotFound, errorResponse{Error: messageNotFound}

	case errors.Is(err, domain.ErrForbidden):
		return http.StatusForbidden, errorResponse{
			Error:   messageForbidden,
			Details: "You can only delete your own avatars",
		}

	case errors.Is(err, domain.ErrTooLarge):
		return http.StatusRequestEntityTooLarge, errorResponse{
			Error:   messageTooLarge,
			MaxSize: a.maxUpload,
		}

	case errors.Is(err, domain.ErrUnsupportedFormat):
		return http.StatusBadRequest, errorResponse{
			Error:   messageInvalidFormat,
			Details: "Supported formats: jpeg, png, webp",
		}

	case errors.Is(err, domain.ErrImageTooBig):
		return http.StatusBadRequest, errorResponse{
			Error:   messageInvalidFormat,
			Details: "Image has too many pixels",
		}

	case errors.Is(err, domain.ErrUnsupportedSize):
		return http.StatusBadRequest, errorResponse{
			Error:   messageBadRequest,
			Details: "Supported sizes: " + supportedSizes(),
		}

	case errors.Is(err, errMissingUserID):
		return http.StatusBadRequest, errorResponse{
			Error:   messageBadRequest,
			Details: headerUserID + " header is required",
		}

	case errors.Is(err, errInvalidAvatarID):
		return http.StatusBadRequest, errorResponse{
			Error:   messageBadRequest,
			Details: "Avatar id must be a UUID",
		}

	case errors.Is(err, errMissingFile):
		return http.StatusBadRequest, errorResponse{
			Error:   messageBadRequest,
			Details: "Attach the image as the file field",
		}

	case errors.Is(err, errMalformedBody):
		return http.StatusBadRequest, errorResponse{
			Error:   messageBadRequest,
			Details: "Body must be multipart/form-data",
		}

	case errors.Is(err, domain.ErrInvalidTransition):
		// Запись сменила состояние между чтением и записью: повтор запроса
		// имеет смысл, а сервер здесь ни при чём.
		return http.StatusConflict, errorResponse{Error: messageConflict}

	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout, errorResponse{Error: messageTimeout}

	default:
		return http.StatusInternalServerError, errorResponse{Error: messageInternal}
	}
}

// supportedSizes перечисляет значения параметра size. Список собирается
// из доменных размеров, чтобы не разойтись с ними при добавлении нового.
func supportedSizes() string {
	sizes := domain.ThumbnailSizes()
	values := make([]string, 0, len(sizes)+1)

	for _, size := range sizes {
		values = append(values, string(size))
	}

	return strings.Join(append(values, domain.SizeOriginal), ", ")
}

// writeJSON отдаёт тело ответа в JSON.
//
// Ошибка кодирования только логируется: заголовки уже отправлены, статус
// не поменять, и сообщить о ней клиенту нечем.
func writeJSON(ctx context.Context, w http.ResponseWriter, log *slog.Logger, status int, body any) {
	w.Header().Set(headerContentType, contentTypeJSON)
	w.WriteHeader(status)

	if err := json.NewEncoder(w).Encode(body); err != nil {
		log.ErrorContext(ctx, "write response body", slog.Any("error", err))
	}
}
