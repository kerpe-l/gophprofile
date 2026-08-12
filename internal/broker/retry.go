package broker

import (
	"math"

	amqp "github.com/rabbitmq/amqp091-go"
)

// maxAttempts — сколько раз сообщение обрабатывается, прежде чем уйти
// в очередь мёртвых.
const maxAttempts = 5

// countField — имя счётчика в записи истории отклонений.
const countField = "count"

// attemptNumber возвращает номер текущей попытки обработки, считая с единицы.
//
// Счётчик консьюмер ведёт сам: выставленный клиентом x-death брокер не сохраняет
// и собирает историю заново, так что у перекладываемого со ступени на ступень
// сообщения она всегда показывает одно отклонение. x-death нужен только там,
// где своего счётчика ещё нет: у сообщения, отклонённого основной очередью
// мимо консьюмера.
func attemptNumber(headers amqp.Table) int {
	if attempts, ok := headerInt(headers, headerAttempts); ok {
		return attempts + 1
	}

	return deathCount(headers) + 1
}

// isFinalAttempt отвечает, будет ли повтор после провала этой попытки.
func isFinalAttempt(attempt int) bool {
	return attempt >= maxAttempts
}

// retryLevelFor выбирает ступень задержки по номеру попытки: первый повтор
// уходит на минимальную задержку, всё сверх длины лестницы — на максимальную.
// Лестница не бывает пустой.
func retryLevelFor(levels []retryLevel, attempt int) retryLevel {
	index := attempt - 1

	if index < 0 {
		index = 0
	}

	if index >= len(levels) {
		index = len(levels) - 1
	}

	return levels[index]
}

// deathCount складывает счётчики из истории отклонений сообщения.
// Записи неожиданного вида пропускаются: худшее, что даст пропуск, —
// лишний прогон по лестнице.
func deathCount(headers amqp.Table) int {
	entries, ok := headers[argDeath].([]any)
	if !ok {
		return 0
	}

	total := 0

	for _, entry := range entries {
		table, ok := entry.(amqp.Table)
		if !ok {
			continue
		}

		count, ok := headerInt(table, countField)
		if !ok {
			continue
		}

		total += count
	}

	return total
}

// headerInt читает неотрицательное целое из таблицы заголовков. Ширину типа
// выбирает тот, кто заголовок записал: брокер отдаёт счётчики int64,
// но клиенты кладут числа и меньшими типами.
func headerInt(headers amqp.Table, name string) (int, bool) {
	var value int64

	switch v := headers[name].(type) {
	case int64:
		value = v
	case int32:
		value = int64(v)
	case int16:
		value = int64(v)
	case int:
		value = int64(v)
	default:
		return 0, false
	}

	if value <= 0 || value > math.MaxInt32 {
		return 0, false
	}

	return int(value), true
}
