package http

import (
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/kerpe-l/gophprofile/internal/server/service"
)

// writeContent отдаёт изображение клиенту, закрывая тело в любом исходе:
// до закрытия заняты соединение с хранилищем и горутина чтения внутри клиента.
//
// Тело не буферизуется: изображение переливается из хранилища в ответ.
func (a *api) writeContent(w http.ResponseWriter, r *http.Request, content service.Content) {
	ctx := r.Context()

	defer func() {
		if err := content.Body.Close(); err != nil {
			a.log.WarnContext(ctx, "close image body", slog.Any("error", err))
		}
	}()

	header := w.Header()
	etag := quoteETag(content.ETag)

	if etag != "" {
		header.Set(headerETag, etag)
	}

	header.Set(headerCacheControl, "max-age="+strconv.Itoa(int(content.MaxAge.Seconds())))

	// Клиент отличает заглушку от настоящего аватара только по этому заголовку:
	// в остальном ответ выглядит как обычное изображение.
	if content.IsDefault {
		header.Set(headerAvatarDefault, "true")
	}

	if matchETag(r.Header.Get(headerIfNoneMatch), etag) {
		w.WriteHeader(http.StatusNotModified)

		return
	}

	header.Set(headerContentType, content.ContentType)
	header.Set(headerContentLength, strconv.FormatInt(content.Size, 10))

	if _, err := io.Copy(w, content.Body); err != nil {
		// Ответ уже начат: код в нём проставлен, и сообщить об обрыве нечем.
		// Обычная причина — ушедший клиент, вмешательства она не требует.
		a.log.WarnContext(ctx, "stream image", slog.Any("error", err))
	}
}

// quoteETag приводит валидатор к виду заголовка: в кавычках.
// Пустой валидатор остаётся пустым — заголовок с ним не выставляется.
func quoteETag(etag string) string {
	if etag == "" || strings.HasPrefix(etag, `"`) {
		return etag
	}

	return `"` + etag + `"`
}

// matchETag сверяет значение If-None-Match с валидатором ответа.
//
// Заголовок может содержать список валидаторов, звёздочку или слабые
// валидаторы с префиксом W/: сравнение здесь слабое, потому что тело
// по одному ключу хранилища не меняется — новая загрузка порождает новый
// аватар с новым ключом.
func matchETag(ifNoneMatch, etag string) bool {
	if ifNoneMatch == "" || etag == "" {
		return false
	}

	for candidate := range strings.SplitSeq(ifNoneMatch, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" {
			return true
		}

		if strings.TrimPrefix(candidate, "W/") == etag {
			return true
		}
	}

	return false
}
