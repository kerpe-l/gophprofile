package broker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/kerpe-l/gophprofile/internal/logger"
)

// ErrNonRetryable — ошибка, которую не исправит повтор: битый файл,
// неподдерживаемый формат, удалённый аватар. Всё, что в неё не обёрнуто,
// считается повторяемым.
var ErrNonRetryable = errors.New("non-retryable error")

// Message — доставленное событие.
type Message struct {
	// Type — RoutingKeyUploaded или RoutingKeyDeleted.
	Type      string
	MessageID string
	// Body — тело события в JSON.
	Body []byte
	// Attempt — номер попытки обработки, считая с единицы.
	Attempt int
	// Final — повтора после провала этой попытки не будет, сообщение уйдёт
	// в очередь мёртвых. Обработчик по нему проставляет терминальный статус:
	// про исчерпание попыток знает только консьюмер, про базу — только он сам.
	Final bool
}

// Handler обрабатывает одно сообщение.
//
// Возврат nil означает успех и подтверждение сообщения. Ошибка, обёрнутая
// в ErrNonRetryable, тоже подтверждает сообщение, но без повтора; любая другая
// отправляет сообщение на следующую ступень лестницы повторов.
type Handler func(ctx context.Context, msg Message) error

// Consumer читает основную очередь и раскладывает сообщения по лестнице
// повторов и очереди мёртвых.
type Consumer struct {
	ch     *amqp.Channel
	pub    *Publisher
	log    *slog.Logger
	tracer trace.Tracer
	// levels — лестница повторов от минимальной задержки к максимальной.
	levels []retryLevel
	// prefetch — сколько сообщений брокер отдаёт не дожидаясь подтверждений;
	// столько же горутин их и разбирает.
	prefetch int
	timeout  time.Duration
}

// Consumer открывает консьюмера основной очереди. Консьюмер обязан быть
// закрыт вызывающим, если живёт меньше соединения.
func (c *Conn) Consumer(log *slog.Logger) (*Consumer, error) {
	ch, err := c.conn.Channel()
	if err != nil {
		return nil, fmt.Errorf("open consume channel: %w", err)
	}

	return &Consumer{
		ch: ch,
		// Перекладывание сообщения идёт по своему каналу: на канале
		// с подтверждениями публикаций подтверждения доставок не ждут.
		pub:      c.Publisher(),
		log:      log,
		tracer:   otel.Tracer(tracerName),
		levels:   c.levels,
		prefetch: c.prefetch,
		timeout:  c.processTimeout,
	}, nil
}

// Close закрывает каналы консьюмера. Неподтверждённые сообщения брокер
// возвращает в очередь сам.
func (c *Consumer) Close() error {
	return errors.Join(c.pub.Close(), closeChannel(c.ch))
}

// Consume читает очередь, пока не отменят контекст или не оборвётся канал.
//
// Отмена контекста прекращает приём новых сообщений; уже взятые в работу
// дорабатываются, и только после этого Consume возвращает nil. Обрыв канала
// или соединения — ошибка.
func (c *Consumer) Consume(ctx context.Context, handler Handler) error {
	if err := c.ch.Qos(c.prefetch, 0, false); err != nil {
		return fmt.Errorf("set prefetch %d: %w", c.prefetch, err)
	}

	deliveries, err := c.ch.Consume(queueProcess, "", false, false, false, false, nil)
	if err != nil {
		return fmt.Errorf("consume queue %s: %w", queueProcess, err)
	}

	closed := c.ch.NotifyClose(make(chan *amqp.Error, 1))

	jobs := make(chan amqp.Delivery)

	var wg sync.WaitGroup

	for range c.prefetch {
		wg.Go(func() {
			for delivery := range jobs {
				c.process(ctx, handler, delivery)
			}
		})
	}

	err = c.pump(ctx, deliveries, closed, jobs)

	wg.Wait()

	return err
}

// pump раздаёт доставки разборщикам и закрывает очередь работ на выходе:
// закрытый канал плюс перебор по нему дают предсказуемое завершение, при
// котором ни один разборщик не выходит с сообщением на руках.
func (c *Consumer) pump(
	ctx context.Context,
	deliveries <-chan amqp.Delivery,
	closed <-chan *amqp.Error,
	jobs chan<- amqp.Delivery,
) error {
	defer close(jobs)

	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-closed:
			return fmt.Errorf("consume queue %s: %w", queueProcess, err)
		case delivery, ok := <-deliveries:
			if !ok {
				return fmt.Errorf("consume queue %s: deliveries stopped", queueProcess)
			}

			select {
			case jobs <- delivery:
			case <-ctx.Done():
				return nil
			}
		}
	}
}

// process отдаёт сообщение обработчику и решает его судьбу по возвращённой
// ошибке.
func (c *Consumer) process(ctx context.Context, handler Handler, delivery amqp.Delivery) {
	attempt := attemptNumber(delivery.Headers)
	msg := Message{
		Type:      delivery.Type,
		MessageID: delivery.MessageId,
		Body:      delivery.Body,
		Attempt:   attempt,
		Final:     isFinalAttempt(attempt),
	}

	// Сигнал завершения прекращает приём новых сообщений, но не работу над
	// взятым: брошенная на середине обработка оставила бы в хранилище часть
	// миниатюр, а запись — в статусе «обрабатывается».
	ctx = context.WithoutCancel(ctx)

	if delivery.CorrelationId != "" {
		ctx = logger.WithRequestID(ctx, delivery.CorrelationId)
	}

	// Контекст трейса приезжает в заголовках сообщения: consumer-спан
	// продолжает трейс запроса, породившего событие.
	ctx = otel.GetTextMapPropagator().Extract(ctx, headerCarrier(delivery.Headers))

	ctx, span := c.tracer.Start(ctx, "process "+msg.Type,
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(
			semconv.MessagingSystemRabbitMQ,
			semconv.MessagingDestinationName(queueProcess),
			semconv.MessagingMessageID(msg.MessageID),
			attribute.Int("attempt", attempt),
		))
	defer span.End()

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	err := handler(ctx, msg)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}

	switch {
	case err == nil:
		c.ack(ctx, delivery)
	case errors.Is(err, ErrNonRetryable):
		span.SetAttributes(attribute.String("outcome", "dropped"))
		c.log.ErrorContext(ctx, "message dropped",
			slog.Any("error", err),
			slog.String("type", msg.Type),
			slog.String("message_id", msg.MessageID),
		)
		c.ack(ctx, delivery)
	case msg.Final:
		span.SetAttributes(attribute.String("outcome", "dead_letter"))
		c.log.ErrorContext(ctx, "message moved to dead letter queue",
			slog.Any("error", err),
			slog.String("type", msg.Type),
			slog.String("message_id", msg.MessageID),
			slog.Int("attempt", attempt),
		)
		c.reroute(ctx, exchangeDead, routingKeyDead, delivery, attempt)
	default:
		span.SetAttributes(attribute.String("outcome", "retry"))

		level := retryLevelFor(c.levels, attempt)
		c.log.WarnContext(ctx, "message scheduled for retry",
			slog.Any("error", err),
			slog.String("type", msg.Type),
			slog.String("message_id", msg.MessageID),
			slog.Int("attempt", attempt),
			slog.Duration("delay", level.ttl),
		)
		c.reroute(ctx, exchangeRetry, level.key, delivery, attempt)
	}
}

// reroute перекладывает сообщение в другую очередь и подтверждает исходное.
//
// Ступень задержки выбирается перепубликацией, а не отклонением: у очереди
// один обмен для отклонённых сообщений, и выбрать по нему одну ступень
// из трёх нельзя.
func (c *Consumer) reroute(ctx context.Context, exchange, key string, delivery amqp.Delivery, attempt int) {
	if err := c.forward(ctx, exchange, key, delivery, attempt); err != nil {
		c.log.ErrorContext(ctx, "reroute message",
			slog.Any("error", err),
			slog.String("exchange", exchange),
			slog.String("message_id", delivery.MessageId),
		)

		// Отклонённое сообщение уходит на первую ступень лестницы: она
		// привязана и к ключам событий тоже. Хуже лишнего повтора только
		// потерянное сообщение.
		if err := delivery.Reject(false); err != nil {
			c.log.ErrorContext(ctx, "reject message",
				slog.Any("error", err),
				slog.String("message_id", delivery.MessageId),
			)
		}

		return
	}

	c.ack(ctx, delivery)
}

// forward публикует сообщение заново, записав в заголовки число сделанных
// попыток: без него следующая доставка выглядела бы первой, и лестница
// повторов не заканчивалась бы никогда.
func (c *Consumer) forward(ctx context.Context, exchange, key string, delivery amqp.Delivery, attempt int) error {
	headers := amqp.Table{}
	for name, value := range delivery.Headers {
		headers[name] = value
	}

	headers[headerAttempts] = int64(attempt)

	return c.pub.publish(ctx, exchange, key, amqp.Publishing{
		Headers:       headers,
		ContentType:   delivery.ContentType,
		DeliveryMode:  amqp.Persistent,
		MessageId:     delivery.MessageId,
		CorrelationId: delivery.CorrelationId,
		Type:          delivery.Type,
		Timestamp:     delivery.Timestamp,
		Body:          delivery.Body,
	})
}

// ack подтверждает сообщение. Неподтверждённое сообщение брокер доставит
// повторно, поэтому отказ Ack записывается и в спан обработки.
func (c *Consumer) ack(ctx context.Context, delivery amqp.Delivery) {
	if err := delivery.Ack(false); err != nil {
		span := trace.SpanFromContext(ctx)
		span.RecordError(err)
		span.SetStatus(codes.Error, "acknowledge message: "+err.Error())
		span.SetAttributes(attribute.String("outcome", "ack_failed"))

		c.log.ErrorContext(ctx, "acknowledge message",
			slog.Any("error", err),
			slog.String("message_id", delivery.MessageId),
		)
	}
}
