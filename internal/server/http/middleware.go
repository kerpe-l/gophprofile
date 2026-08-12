package http

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/google/uuid"

	"github.com/kerpe-l/gophprofile/internal/logger"
)

// maxRequestIDLen — предел длины идентификатора запроса от клиента: значение
// попадает в каждую запись лога.
const maxRequestIDLen = 64

// requestID связывает запрос с его идентификатором: кладёт в контекст,
// откуда его подхватывает логгер, и возвращает клиенту тем же заголовком.
func requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(headerRequestID)
		if !validRequestID(id) {
			id = uuid.NewString()
		}

		w.Header().Set(headerRequestID, id)

		next.ServeHTTP(w, r.WithContext(logger.WithRequestID(r.Context(), id)))
	})
}

// validRequestID разрешает непустую строку разумной длины из печатаемых
// ASCII-символов.
func validRequestID(id string) bool {
	if id == "" || len(id) > maxRequestIDLen {
		return false
	}

	for _, c := range id {
		if c < ' ' || c > '~' {
			return false
		}
	}

	return true
}

// logging пишет одну запись на запрос — с кодом ответа и длительностью.
func logging(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			started := time.Now()
			rec := &recorder{ResponseWriter: w, status: http.StatusOK}

			next.ServeHTTP(rec, r)

			level := slog.LevelInfo
			if rec.status >= http.StatusInternalServerError {
				level = slog.LevelError
			}

			log.Log(r.Context(), level, "request handled",
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", rec.status),
				slog.Int64("bytes", rec.written),
				slog.Duration("took", time.Since(started)),
			)
		})
	}
}

// recovering не даёт панике в хендлере уронить процесс.
func recovering(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()

			defer func() {
				cause := recover()
				if cause == nil {
					return
				}

				// http.ErrAbortHandler — договорённый способ оборвать ответ
				// молча; сервер разбирает её сам, и перехват здесь превратил бы
				// штатный обрыв в запись об ошибке.
				if err, ok := cause.(error); ok && errors.Is(err, http.ErrAbortHandler) {
					panic(cause)
				}

				log.ErrorContext(ctx, "panic in handler",
					slog.Any("panic", cause),
					slog.String("method", r.Method),
					slog.String("path", r.URL.Path),
					slog.String("stack", string(debug.Stack())),
				)

				// Ответ, который уже начат, дописывать нечем: код в нём
				// проставлен, а тело содержит часть изображения.
				if tracker, ok := w.(interface{ headerWritten() bool }); ok && tracker.headerWritten() {
					return
				}

				writeJSON(ctx, w, log, http.StatusInternalServerError,
					errorResponse{Error: messageInternal})
			}()

			next.ServeHTTP(w, r)
		})
	}
}

// timeout ограничивает время обработки запроса. Дедлайн ставится здесь,
// а не в хендлерах: иначе следующий добавленный хендлер окажется без него.
func timeout(d time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), d)
			defer cancel()

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// maxBytes ограничивает тело запроса до разбора, а не после вычитывания.
func maxBytes(limit int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, limit)

			next.ServeHTTP(w, r)
		})
	}
}

// extendWriteDeadline отодвигает дедлайн записи ответа. WriteTimeout сервера
// отсчитывается от конца чтения заголовков, поэтому на медленной заливке
// он истекает раньше, чем есть что записать.
func extendWriteDeadline(d time.Duration, log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(d)); err != nil {
				// Без продления запрос всё равно обрабатывается — не запишется
				// разве что ответ на самую медленную заливку.
				log.WarnContext(r.Context(), "extend write deadline", slog.Any("error", err))
			}

			next.ServeHTTP(w, r)
		})
	}
}

// recorder запоминает код ответа и объём тела для записи в лог.
type recorder struct {
	http.ResponseWriter
	status  int
	written int64
	wrote   bool
}

// WriteHeader запоминает первый выставленный код ответа.
func (r *recorder) WriteHeader(status int) {
	if r.wrote {
		return
	}

	r.status = status
	r.wrote = true

	r.ResponseWriter.WriteHeader(status)
}

// Write считает отданные байты, проставляя код по умолчанию так же,
// как это сделал бы сервер.
func (r *recorder) Write(b []byte) (int, error) {
	r.wrote = true

	n, err := r.ResponseWriter.Write(b)
	r.written += int64(n)

	return n, err
}

// headerWritten сообщает, начат ли ответ. Обработчик паники по нему решает,
// есть ли ещё смысл писать тело с ошибкой.
func (r *recorder) headerWritten() bool {
	return r.wrote
}

// Unwrap открывает http.ResponseController доступ к исходному ResponseWriter.
func (r *recorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}
