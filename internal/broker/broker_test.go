package broker_test

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kerpe-l/gophprofile/internal/broker"
	"github.com/kerpe-l/gophprofile/internal/config"
)

// shortTimeout — предел в тестах, где важен не результат, а то, что вызов
// вообще возвращается.
const shortTimeout = 200 * time.Millisecond

// listen открывает слушателя на свободном порту.
func listen(t *testing.T) (net.Listener, error) {
	t.Helper()

	return (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
}

func brokerConfig(addr string) config.AMQP {
	return config.AMQP{
		URL:      "amqp://guest:guest@" + addr + "/",
		Prefetch: 1,
		Timeout:  shortTimeout,
	}
}

// TestNewHangingBroker — брокер принял соединение и молчит. Рукопожатие AMQP
// идёт уже на установленном соединении, и без собственного предела старт
// сервиса завис бы на нём навсегда.
func TestNewHangingBroker(t *testing.T) {
	t.Parallel()

	listener, err := listen(t)
	require.NoError(t, err)

	t.Cleanup(func() {
		assert.NoError(t, listener.Close())
	})

	accepted := make(chan net.Conn, 1)

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}

		accepted <- conn
	}()

	done := make(chan error, 1)

	go func() {
		ctx, cancel := context.WithTimeout(t.Context(), shortTimeout)
		defer cancel()

		conn, err := broker.New(ctx, brokerConfig(listener.Addr().String()))
		if err == nil {
			done <- conn.Close()

			return
		}

		done <- err
	}()

	select {
	case err := <-done:
		require.Error(t, err, "рукопожатие с молчащим брокером должно завершиться ошибкой")
	case <-time.After(10 * time.Second):
		t.Fatal("подключение к неотвечающему брокеру не вернулось")
	}

	select {
	case conn := <-accepted:
		assert.NoError(t, conn.Close())
	default:
	}
}

func TestNewUnreachableBroker(t *testing.T) {
	t.Parallel()

	// Порт занят и тут же освобождён: подключение к нему заведомо не удастся.
	listener, err := listen(t)
	require.NoError(t, err)

	addr := listener.Addr().String()
	require.NoError(t, listener.Close())

	ctx, cancel := context.WithTimeout(t.Context(), shortTimeout)
	defer cancel()

	conn, err := broker.New(ctx, brokerConfig(addr))
	require.Error(t, err)
	assert.Nil(t, conn)
	// Строка подключения содержит учётные данные и наружу уходить не должна.
	assert.NotContains(t, err.Error(), "guest")
}
