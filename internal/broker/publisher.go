package broker

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/kerpe-l/gophprofile/internal/logger"
)

// contentTypeJSON — тип содержимого сообщений.
const contentTypeJSON = "application/json"

// Publisher отправляет события в брокер и ждёт подтверждения от него.
// Публикации сериализуются: канал AMQP не рассчитан на параллельную запись.
type Publisher struct {
	conn *amqp.Connection
	// timeout — предел на одну публикацию вместе с ожиданием подтверждения.
	timeout time.Duration

	mu sync.Mutex
	// ch открывается при первой публикации и переоткрывается, если брокер
	// закрыл его; nil означает, что канала ещё или уже нет.
	ch *amqp.Channel
	// returns — сообщения, которые брокер не смог никуда направить.
	returns <-chan amqp.Return
}

// Publisher возвращает публикатора событий. Канал он открывает при первой
// публикации; отдельно закрывать публикатора нужно только там, где он живёт
// меньше соединения.
func (c *Conn) Publisher() *Publisher {
	return &Publisher{conn: c.conn, timeout: c.timeout}
}

// Publish отправляет событие и возвращается только после подтверждения
// брокером. Идентификатор запроса из контекста уезжает вместе с сообщением,
// чтобы записи обработчика в логе связывались с породившим их запросом.
func (p *Publisher) Publish(ctx context.Context, event Event) error {
	body, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("encode event %s: %w", event.ID(), err)
	}

	msg := amqp.Publishing{
		ContentType:   contentTypeJSON,
		DeliveryMode:  amqp.Persistent,
		MessageId:     event.ID(),
		CorrelationId: logger.RequestID(ctx),
		Type:          event.RoutingKey(),
		Timestamp:     time.Now(),
		Body:          body,
	}

	if err := p.publish(ctx, exchangeEvents, event.RoutingKey(), msg); err != nil {
		return fmt.Errorf("publish event %s: %w", event.ID(), err)
	}

	return nil
}

// Close закрывает канал публикатора. Повторный вызов безопасен.
func (p *Publisher) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.ch == nil {
		return nil
	}

	err := closeChannel(p.ch)
	p.ch = nil

	return err
}

// publish отправляет уже собранное сообщение. Единственное место, где
// на обращение к брокеру ставится предел времени: дедлайн вызывающего
// не перебивается.
func (p *Publisher) publish(ctx context.Context, exchange, key string, msg amqp.Publishing) error {
	ctx, cancel := withDeadline(ctx, p.timeout)
	defer cancel()

	p.mu.Lock()
	defer p.mu.Unlock()

	ch, err := p.openLocked()
	if err != nil {
		return err
	}

	// Без подтверждения успешная публикация означает лишь «отдали в сокет»:
	// брокер мог не принять сообщение вовсе. mandatory заставляет его вернуть
	// сообщение, которое не попало ни в одну очередь, — иначе оно пропадает
	// молча.
	confirm, err := ch.PublishWithDeferredConfirmWithContext(ctx, exchange, key, true, false, msg)
	if err != nil {
		p.dropLocked()

		return fmt.Errorf("publish to %s with key %s: %w", exchange, key, err)
	}

	acked, err := confirm.WaitContext(ctx)
	if err != nil {
		return fmt.Errorf("confirm publish to %s with key %s: %w", exchange, key, err)
	}

	if !acked {
		return fmt.Errorf("publish to %s with key %s: rejected by broker", exchange, key)
	}

	// Возврат брокер присылает раньше подтверждения, поэтому проверка
	// неблокирующая.
	select {
	case ret := <-p.returns:
		return fmt.Errorf("publish to %s with key %s: unroutable (%d %s)", exchange, key, ret.ReplyCode, ret.ReplyText)
	default:
		return nil
	}
}

// openLocked возвращает канал публикации, открывая его при необходимости.
// Брокер закрывает канал на протокольной ошибке при живом соединении, и без
// переоткрытия публикатор остался бы мёртвым до перезапуска процесса.
func (p *Publisher) openLocked() (*amqp.Channel, error) {
	if p.ch != nil && !p.ch.IsClosed() {
		return p.ch, nil
	}

	ch, err := p.conn.Channel()
	if err != nil {
		return nil, fmt.Errorf("open publish channel: %w", err)
	}

	if err := ch.Confirm(false); err != nil {
		// Причина отказа уже есть; ошибка закрытия её не уточняет.
		_ = ch.Close()

		return nil, fmt.Errorf("enable publisher confirms: %w", err)
	}

	p.ch = ch
	p.returns = ch.NotifyReturn(make(chan amqp.Return, 1))

	return ch, nil
}

// dropLocked забывает канал, на котором сорвалась публикация: следующая
// откроет новый.
func (p *Publisher) dropLocked() {
	if p.ch == nil {
		return
	}

	// Ошибка закрытия уже неисправного канала ни на что не влияет.
	_ = p.ch.Close()
	p.ch = nil
	p.returns = nil
}
