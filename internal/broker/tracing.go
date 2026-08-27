package broker

import (
	amqp "github.com/rabbitmq/amqp091-go"
)

// tracerName — имя инструментации пакета в трейсах.
const tracerName = "github.com/kerpe-l/gophprofile/internal/broker"

// headerCarrier переносит контекст трейса через заголовки сообщения AMQP:
// publisher вписывает traceparent при публикации, consumer вычитывает его
// из доставки.
type headerCarrier amqp.Table

// Get возвращает строковое значение заголовка; значения других типов
// неотличимы от отсутствующих.
func (c headerCarrier) Get(key string) string {
	value, ok := c[key].(string)
	if !ok {
		return ""
	}

	return value
}

func (c headerCarrier) Set(key, value string) {
	c[key] = value
}

func (c headerCarrier) Keys() []string {
	keys := make([]string, 0, len(c))
	for key := range c {
		keys = append(keys, key)
	}

	return keys
}
