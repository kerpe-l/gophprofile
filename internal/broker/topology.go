package broker

import (
	"fmt"
	"strings"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// Обмены и очереди.
const (
	// exchangeEvents — обмен событий аватаров, topic.
	exchangeEvents = "avatars.exchange"
	// exchangeRetry — обмен лестницы повторов, direct: ключ выбирает ступень.
	exchangeRetry = "avatars.retry"
	// exchangeDead — обмен очереди мёртвых сообщений, direct.
	exchangeDead = "avatars.dlq"

	// queueProcess — основная очередь обработки.
	queueProcess = "avatars.process"
	// queueDead — очередь сообщений, исчерпавших попытки.
	queueDead = "avatars.dead"
	// queueRetryPrefix — начало имени очереди ступени: avatars.retry.5s.
	queueRetryPrefix = "avatars.retry."
	// retryKeyPrefix — начало ключа ступени: retry.5s.
	retryKeyPrefix = "retry."
)

// Routing keys.
const (
	// RoutingKeyUploaded — событие «оригинал загружен, нужны миниатюры».
	RoutingKeyUploaded = "avatar.uploaded"
	// RoutingKeyDeleted — событие «аватар удалён, файлы надо убрать».
	RoutingKeyDeleted = "avatar.deleted"

	// routingKeyRetried — ключ, с которым сообщение возвращается с лестницы
	// повторов. Исходный ключ к этому моменту потерян: у очереди ступени один
	// ключ возврата на все события. Тип события едет отдельным свойством
	// сообщения и от ключа не зависит.
	routingKeyRetried = "avatar.retried"
	// routingKeyDead — ключ очереди мёртвых сообщений.
	routingKeyDead = "avatar.dead"

	// bindingEvents — привязка основной очереди: и первичные события,
	// и возврат с лестницы повторов.
	bindingEvents = "avatar.*"
)

// Аргументы очередей.
const (
	argDeadLetterExchange   = "x-dead-letter-exchange"
	argDeadLetterRoutingKey = "x-dead-letter-routing-key"
	argMessageTTL           = "x-message-ttl"
	// argDeath — заголовок, в котором брокер ведёт историю отклонений
	// и истечений срока жизни сообщения.
	argDeath = "x-death"
	// headerAttempts — счётчик уже сделанных попыток обработки. Свой, потому
	// что историю отклонений брокер выставленную клиентом не сохраняет.
	headerAttempts = "x-avatar-attempts"
)

// retryLevel — ступень лестницы повторов.
type retryLevel struct {
	// queue — очередь ступени: сообщение лежит в ней, пока не истечёт ttl.
	queue string
	// key — ключ, которым сообщение кладётся на эту ступень.
	key string
	// ttl — задержка перед возвратом в основную очередь.
	ttl time.Duration
}

// defaultRetryDelays — стандартная лестница задержек.
func defaultRetryDelays() []time.Duration {
	return []time.Duration{
		5 * time.Second,
		30 * time.Second,
		5 * time.Minute,
	}
}

// newRetryLevels собирает ступени по задержкам. Имя очереди и ключ ступени
// называют её задержку, разойтись они не могут.
func newRetryLevels(delays []time.Duration) []retryLevel {
	levels := make([]retryLevel, 0, len(delays))

	for _, delay := range delays {
		name := delayName(delay)
		levels = append(levels, retryLevel{
			queue: queueRetryPrefix + name,
			key:   retryKeyPrefix + name,
			ttl:   delay,
		})
	}

	return levels
}

// delayName — короткое имя задержки: 5s, 30s, 5m.
func delayName(d time.Duration) string {
	name := d.String()

	// Минуты печатаются вместе с нулевыми секундами: 5m0s.
	if strings.HasSuffix(name, "m0s") {
		return strings.TrimSuffix(name, "0s")
	}

	return name
}

// declareTopology объявляет обмены, очереди и привязки. Объявление
// идемпотентно, пока параметры совпадают с уже объявленными.
func declareTopology(ch *amqp.Channel, levels []retryLevel) error {
	exchanges := []struct {
		name string
		kind string
	}{
		{exchangeEvents, amqp.ExchangeTopic},
		{exchangeRetry, amqp.ExchangeDirect},
		{exchangeDead, amqp.ExchangeDirect},
	}

	for _, e := range exchanges {
		if err := ch.ExchangeDeclare(e.name, e.kind, true, false, false, false, nil); err != nil {
			return fmt.Errorf("declare exchange %s: %w", e.name, err)
		}
	}

	// Отклонённое сообщение уходит с основной очереди на лестницу повторов.
	err := declareQueue(ch, queueProcess, amqp.Table{argDeadLetterExchange: exchangeRetry})
	if err != nil {
		return err
	}

	if err := bindQueue(ch, queueProcess, exchangeEvents, bindingEvents); err != nil {
		return err
	}

	if err := declareRetryLevels(ch, levels); err != nil {
		return err
	}

	if err := declareQueue(ch, queueDead, nil); err != nil {
		return err
	}

	return bindQueue(ch, queueDead, exchangeDead, routingKeyDead)
}

// declareRetryLevels объявляет очереди ступеней. Каждая по истечении времени
// жизни возвращает сообщение в обмен событий, откуда оно снова попадает
// в основную очередь.
func declareRetryLevels(ch *amqp.Channel, levels []retryLevel) error {
	for i, level := range levels {
		args := amqp.Table{
			argMessageTTL:           level.ttl.Milliseconds(),
			argDeadLetterExchange:   exchangeEvents,
			argDeadLetterRoutingKey: routingKeyRetried,
		}

		if err := declareQueue(ch, level.queue, args); err != nil {
			return err
		}

		keys := []string{level.key}

		// Первая ступень принимает и то, что основная очередь отклонила сама:
		// у очереди один обмен для отклонённых сообщений и ключ при этом
		// сохраняется исходный, поэтому без этих привязок отклонённое
		// сообщение осело бы в обмене, где его никто не ждёт.
		if i == 0 {
			keys = append(keys, RoutingKeyUploaded, RoutingKeyDeleted, routingKeyRetried)
		}

		for _, key := range keys {
			if err := bindQueue(ch, level.queue, exchangeRetry, key); err != nil {
				return err
			}
		}
	}

	return nil
}

// declareQueue объявляет долговечную очередь: сообщения об аватарах обязаны
// пережить перезапуск брокера.
func declareQueue(ch *amqp.Channel, name string, args amqp.Table) error {
	if _, err := ch.QueueDeclare(name, true, false, false, false, args); err != nil {
		return fmt.Errorf("declare queue %s: %w", name, err)
	}

	return nil
}

func bindQueue(ch *amqp.Channel, queue, exchange, key string) error {
	if err := ch.QueueBind(queue, key, exchange, false, nil); err != nil {
		return fmt.Errorf("bind queue %s to exchange %s with key %s: %w", queue, exchange, key, err)
	}

	return nil
}
