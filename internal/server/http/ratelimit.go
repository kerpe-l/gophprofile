package http

import (
	"log/slog"
	"math"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/kerpe-l/gophprofile/internal/config"
)

// Хранение бакетов: простаивающий ключ живёт limiterIdleTTL, сметание
// идёт попутно с запросами, не чаще limiterSweepInterval.
const (
	limiterIdleTTL       = 3 * time.Minute
	limiterSweepInterval = time.Minute
)

// limiters — токен-бакеты по ключам запросов.
type limiters struct {
	limit rate.Limit
	burst int
	// now — источник времени; тест подставляет свой.
	now func() time.Time

	mu        sync.Mutex
	visitors  map[string]*visitor
	lastSweep time.Time
}

// visitor — бакет одного ключа и момент его последнего запроса.
type visitor struct {
	lim  *rate.Limiter
	seen time.Time
}

// newLimiters создаёт хранилище бакетов. Нулевой burst выводится из rps:
// двукратный, минимум 1.
func newLimiters(cfg config.RateLimit) *limiters {
	burst := cfg.Burst
	if burst == 0 {
		burst = max(1, int(math.Ceil(2*cfg.RPS)))
	}

	return &limiters{
		limit:    rate.Limit(cfg.RPS),
		burst:    burst,
		now:      time.Now,
		visitors: map[string]*visitor{},
	}
}

// allow сообщает, пропускается ли запрос ключа, и при отказе — через сколько
// секунд появится следующий токен.
func (l *limiters) allow(key string) (ok bool, retryAfter int) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	l.sweep(now)

	v, exists := l.visitors[key]
	if !exists {
		v = &visitor{lim: rate.NewLimiter(l.limit, l.burst)}
		l.visitors[key] = v
	}

	v.seen = now

	if v.lim.AllowN(now, 1) {
		return true, 0
	}

	// Резерв называет момент появления токена и тут же отменяется:
	// отказ не должен тратить будущие токены.
	res := v.lim.ReserveN(now, 1)
	delay := res.DelayFrom(now)
	res.CancelAt(now)

	return false, max(1, int(math.Ceil(delay.Seconds())))
}

// sweep убирает ключи, не появлявшиеся дольше limiterIdleTTL: карта бакетов
// не растёт с числом ключей, побывавших у сервиса за всё время.
func (l *limiters) sweep(now time.Time) {
	if now.Sub(l.lastSweep) < limiterSweepInterval {
		return
	}

	l.lastSweep = now

	for key, v := range l.visitors {
		if now.Sub(v.seen) > limiterIdleTTL {
			delete(l.visitors, key)
		}
	}
}

// rateLimiting отклоняет запросы сверх лимита ключа ответом 429 с Retry-After.
func rateLimiting(l *limiters, log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ok, retryAfter := l.allow(limitKey(r))
			if !ok {
				w.Header().Set(headerRetryAfter, strconv.Itoa(retryAfter))
				writeJSON(r.Context(), w, log, http.StatusTooManyRequests,
					errorResponse{Error: messageTooMany})

				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// limitKey — ключ бакета: значение X-User-ID, без заголовка — IP клиента.
// Заголовок длиннее своей колонки ключом не становится: его пришлёт разве что
// клиент, который дальше валидации всё равно не пройдёт.
func limitKey(r *http.Request) string {
	if id := r.Header.Get(headerUserID); id != "" && len(id) <= maxUserIDLen {
		return id
	}

	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}

	return host
}
