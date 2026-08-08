// Package broker публикует и потребляет события обработки аватаров через RabbitMQ.
//
// Соединение владеет всеми открытыми на нём каналами: Close у Conn закрывает
// и публикатора, и консьюмера.
package broker

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/kerpe-l/gophprofile/internal/config"
)

// Параметры соединения, не выведенные в конфиг.
const (
	// dialTimeout — предел на установку соединения вместе с рукопожатием AMQP.
	// Без него брокер, принявший TCP-соединение и замолчавший, держит старт
	// сервиса бесконечно.
	dialTimeout = 5 * time.Second
	// heartbeat — период контрольных сообщений. По ним обнаруживается пропавший
	// без разрыва соединения брокер: иначе консьюмер молча ждёт доставки,
	// которой уже не будет.
	heartbeat = 10 * time.Second
)

// Conn — соединение с брокером с объявленной топологией.
type Conn struct {
	conn *amqp.Connection
	// levels — лестница повторов от минимальной задержки к максимальной.
	levels []retryLevel
	// prefetch — сколько сообщений консьюмер берёт не подтверждая.
	prefetch int
	// timeout — предел на публикацию, processTimeout — на обработку сообщения.
	timeout        time.Duration
	processTimeout time.Duration
}

// Option меняет параметры соединения, заданные по умолчанию.
type Option func(*Conn)

// WithRetryDelays переопределяет задержки и имена retry-очередей.
// Пустой список сохраняет значения по умолчанию.
func WithRetryDelays(delays ...time.Duration) Option {
	return func(c *Conn) {
		if len(delays) == 0 {
			return
		}

		c.levels = newRetryLevels(delays)
	}
}

// WithProcessTimeout задаёт предел на обработку одного сообщения вместо
// предела публикации: публикация ждёт подтверждения брокером, а обработка
// события загрузки читает оригинал, декодирует его и пишет обратно миниатюры.
// Неположительное значение ничего не меняет.
func WithProcessTimeout(timeout time.Duration) Option {
	return func(c *Conn) {
		if timeout <= 0 {
			return
		}

		c.processTimeout = timeout
	}
}

// New подключается к брокеру и объявляет топологию.
// Соединение обязано быть закрыто вызывающим.
func New(ctx context.Context, cfg config.AMQP, opts ...Option) (*Conn, error) {
	conn, err := amqp.DialConfig(cfg.URL, amqp.Config{
		Heartbeat: heartbeat,
		Locale:    "en_US",
		Dial: func(network, addr string) (net.Conn, error) {
			return dial(ctx, network, addr)
		},
	})
	if err != nil {
		// Адрес брокера в текст ошибки не попадает: он содержит учётные данные.
		return nil, fmt.Errorf("connect to broker: %w", err)
	}

	c := &Conn{
		conn:     conn,
		levels:   newRetryLevels(defaultRetryDelays()),
		prefetch: cfg.Prefetch,
		timeout:  cfg.Timeout,
		// Пока свой предел не задан, обработка укладывается в тот же, что
		// и публикация: консьюмера может и не быть вовсе.
		processTimeout: cfg.Timeout,
	}

	for _, opt := range opts {
		opt(c)
	}

	if err := c.declare(); err != nil {
		// Причина отказа уже есть; ошибка закрытия её не уточняет.
		_ = c.Close()

		return nil, err
	}

	return c, nil
}

// Close закрывает соединение вместе со всеми открытыми на нём каналами.
// Повторный вызов безопасен.
func (c *Conn) Close() error {
	if err := c.conn.Close(); err != nil && !errors.Is(err, amqp.ErrClosed) {
		return fmt.Errorf("close broker connection: %w", err)
	}

	return nil
}

// Ping проверяет, что соединение живо и основная очередь на месте.
func (c *Conn) Ping(ctx context.Context) error {
	if c.conn.IsClosed() {
		return errors.New("ping broker: connection is closed")
	}

	ctx, cancel := withDeadline(ctx, c.timeout)
	defer cancel()

	done := make(chan error, 1)

	// Запросы к брокеру контекста не принимают, поэтому предел ставится снаружи.
	// Горутина не висит дольше, чем живёт соединение: пропажу брокера без
	// разрыва обнаруживают контрольные сообщения.
	go func() {
		done <- c.checkQueue()
	}()

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return fmt.Errorf("ping broker: %w", ctx.Err())
	}
}

// checkQueue убеждается, что основная очередь объявлена. Пассивное объявление
// очередь не создаёт: проверка не должна маскировать не поднятую топологию.
func (c *Conn) checkQueue() (err error) {
	ch, err := c.conn.Channel()
	if err != nil {
		return fmt.Errorf("ping broker: %w", err)
	}

	defer func() {
		if cerr := closeChannel(ch); cerr != nil && err == nil {
			err = cerr
		}
	}()

	if _, err := ch.QueueDeclarePassive(queueProcess, true, false, false, false, nil); err != nil {
		return fmt.Errorf("ping broker: queue %s: %w", queueProcess, err)
	}

	return nil
}

// declare объявляет топологию на отдельном канале: неудачное объявление
// закрывает канал, на котором его сделали, и работать дальше на нём нельзя.
func (c *Conn) declare() (err error) {
	ch, err := c.conn.Channel()
	if err != nil {
		return fmt.Errorf("open channel: %w", err)
	}

	defer func() {
		if cerr := closeChannel(ch); cerr != nil && err == nil {
			err = cerr
		}
	}()

	return declareTopology(ch, c.levels)
}

// dial устанавливает соединение с пределом на всё рукопожатие целиком; клиент
// снимает дедлайн сам, когда оно завершилось. Дедлайн контекста учитывается
// наравне с собственным: рукопожатие идёт уже на установленном соединении
// и отмену контекста само по себе не заметило бы.
func dial(ctx context.Context, network, addr string) (net.Conn, error) {
	conn, err := (&net.Dialer{Timeout: dialTimeout}).DialContext(ctx, network, addr)
	if err != nil {
		return nil, fmt.Errorf("dial broker: %w", err)
	}

	deadline := time.Now().Add(dialTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}

	if err := conn.SetDeadline(deadline); err != nil {
		// Соединение уже открыто: без закрытия оно осталось бы висеть.
		_ = conn.Close()

		return nil, fmt.Errorf("set handshake deadline: %w", err)
	}

	return conn, nil
}

// withDeadline ставит предел времени на операцию с брокером. Ставится
// безусловно — WithTimeout не продлевает более ранний дедлайн вызывающего.
func withDeadline(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, timeout)
}

// closeChannel закрывает канал, не считая ошибкой уже закрытый: канал
// закрывается и сам — брокером на протокольной ошибке или вместе с соединением.
func closeChannel(ch *amqp.Channel) error {
	if err := ch.Close(); err != nil && !errors.Is(err, amqp.ErrClosed) {
		return fmt.Errorf("close channel: %w", err)
	}

	return nil
}
