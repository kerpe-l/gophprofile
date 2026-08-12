package http

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// checkTimeout — предел на одну проверку. Без него /health подвисает вместе
// с проверяемой зависимостью и перестаёт быть индикатором.
const checkTimeout = 2 * time.Second

// Значения в ответе /health.
const (
	statusOK          = "ok"
	statusDegraded    = "degraded"
	statusUnavailable = "unavailable"
)

// healthResponse — состояние сервиса и его зависимостей.
type healthResponse struct {
	Status     string            `json:"status"`
	Components map[string]string `json:"components"`
}

// health отвечает состоянием сервиса: 200, пока все зависимости на месте,
// и 503 с degraded, если отвалилась хотя бы одна. Проверки идут параллельно,
// иначе их пределы складывались бы; горутин ровно столько, сколько проверок.
func (a *api) health(w http.ResponseWriter, r *http.Request) {
	var (
		mu         sync.Mutex
		wg         sync.WaitGroup
		components = make(map[string]string, len(a.checks))
		ctx        = r.Context()
	)

	for name, check := range a.checks {
		wg.Go(func() {
			status := a.checkStatus(ctx, name, check)

			mu.Lock()
			defer mu.Unlock()

			components[name] = status
		})
	}

	wg.Wait()

	response := healthResponse{Status: statusOK, Components: components}
	code := http.StatusOK

	for _, status := range components {
		if status != statusOK {
			response.Status = statusDegraded
			code = http.StatusServiceUnavailable

			break
		}
	}

	writeJSON(ctx, w, a.log, code, response)
}

// checkStatus опрашивает одну зависимость. Причина отказа уходит в лог:
// наружу отдавать её нельзя — в ней адреса, имена бакетов и очередей.
func (a *api) checkStatus(ctx context.Context, name string, check Checker) string {
	// Собственный контекст, а не контекст запроса: отменённый ушедшим клиентом
	// запрос не должен превращать живую зависимость в недоступную, а предел
	// времени у проверки свой.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), checkTimeout)
	defer cancel()

	if err := check.Ping(ctx); err != nil {
		a.log.WarnContext(ctx, "dependency is unavailable",
			slog.String("component", name), slog.Any("error", err))

		return statusUnavailable
	}

	return statusOK
}
