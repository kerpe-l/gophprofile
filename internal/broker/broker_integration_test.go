//go:build integration

package broker_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/suite"
	"github.com/testcontainers/testcontainers-go"
	tcrabbitmq "github.com/testcontainers/testcontainers-go/modules/rabbitmq"

	"github.com/kerpe-l/gophprofile/internal/broker"
	"github.com/kerpe-l/gophprofile/internal/config"
	"github.com/kerpe-l/gophprofile/internal/logger"
)

// rabbitImage — та же версия, что в docker-compose.yml.
const rabbitImage = "rabbitmq:4-management-alpine"

// deadQueue — очередь мёртвых сообщений; тест читает её мимо проверяемого кода.
const deadQueue = "avatars.dead"

// waitFor — предел ожидания сообщения. Полный проход лестницы на тестовых
// задержках занимает около двух секунд.
const waitFor = 30 * time.Second

// gracePeriod — сколько времени даётся на то, чтобы Consume ошибочно вернулся
// из-под отменённого контекста, не дождавшись начатой обработки.
const gracePeriod = 500 * time.Millisecond

// testDelays — укороченная лестница: со стандартными 5s/30s/5m тест на проход
// до очереди мёртвых ждал бы больше пятнадцати минут.
func testDelays() []time.Duration {
	return []time.Duration{
		200 * time.Millisecond,
		400 * time.Millisecond,
		600 * time.Millisecond,
	}
}

// BrokerSuite поднимает брокер один раз на весь набор: контейнер стартует
// секунды, а тестов много. Изоляция — уникальными идентификаторами сообщений
// и тем, что каждый тест подтверждает всё, что взял.
type BrokerSuite struct {
	suite.Suite

	cfg  config.AMQP
	conn *broker.Conn
	pub  *broker.Publisher
	// raw — соединение мимо проверяемого кода: им читается очередь мёртвых.
	raw *amqp.Connection
}

func TestBroker(t *testing.T) {
	t.Parallel()

	suite.Run(t, new(BrokerSuite))
}

func (s *BrokerSuite) SetupSuite() {
	ctx := s.T().Context()

	container, err := tcrabbitmq.Run(ctx, rabbitImage)
	s.Require().NoError(err)
	testcontainers.CleanupContainer(s.T(), container)

	url, err := container.AmqpURL(ctx)
	s.Require().NoError(err)

	s.cfg = config.AMQP{URL: url, Prefetch: 4, Timeout: 10 * time.Second}

	s.conn, err = broker.New(ctx, s.cfg, broker.WithRetryDelays(testDelays()...))
	s.Require().NoError(err)

	s.pub = s.conn.Publisher()

	s.raw, err = amqp.Dial(url)
	s.Require().NoError(err)
}

func (s *BrokerSuite) TearDownSuite() {
	if s.raw != nil {
		s.NoError(s.raw.Close())
	}

	if s.conn != nil {
		s.NoError(s.conn.Close())
	}
}

// log пишет решения консьюмера в stderr: по ним видно, почему упал тест
// на лестницу повторов.
func (s *BrokerSuite) log() *slog.Logger {
	return logger.New(os.Stderr, slog.LevelWarn, logger.FormatText)
}

// consumer — запущенный консьюмер вместе со средствами его остановки.
type consumer struct {
	// cancel прекращает приём новых сообщений, не дожидаясь завершения.
	cancel context.CancelFunc
	// done отдаёт результат Consume.
	done <-chan error
	// shutdown отменяет контекст и дожидается возврата из Consume.
	shutdown func()
}

// start запускает консьюмера и снимает его по завершении теста.
func (s *BrokerSuite) start(handler broker.Handler) *consumer {
	s.T().Helper()

	c, err := s.conn.Consumer(s.log())
	s.Require().NoError(err)

	ctx, cancel := context.WithCancel(s.T().Context())
	done := make(chan error, 1)

	go func() {
		done <- c.Consume(ctx, handler)
	}()

	var once sync.Once

	shutdown := func() {
		once.Do(func() {
			cancel()
			s.NoError(<-done, "отмена контекста — штатное завершение, а не ошибка")
			s.NoError(c.Close())
		})
	}

	s.T().Cleanup(shutdown)

	return &consumer{cancel: cancel, done: done, shutdown: shutdown}
}

// deadLetters открывает чтение очереди мёртвых сообщений мимо проверяемого кода.
func (s *BrokerSuite) deadLetters() <-chan amqp.Delivery {
	s.T().Helper()

	ch, err := s.raw.Channel()
	s.Require().NoError(err)

	s.T().Cleanup(func() {
		s.NoError(ch.Close())
	})

	deliveries, err := ch.Consume(deadQueue, "", true, false, false, false, nil)
	s.Require().NoError(err)

	return deliveries
}

// publish отправляет событие загрузки и возвращает его.
func (s *BrokerSuite) publish(userID string) broker.AvatarUploadEvent {
	s.T().Helper()

	event := broker.NewUploadEvent(uuid.MustParse(avatarID), userID, "originals/"+avatarID)
	s.Require().NoError(s.pub.Publish(s.T().Context(), event))

	return event
}

// Объявление топологии делает каждый бинарник при старте, в том числе поверх
// уже поднятой другим.
func (s *BrokerSuite) TestDeclareIsIdempotent() {
	conn, err := broker.New(s.T().Context(), s.cfg, broker.WithRetryDelays(testDelays()...))
	s.Require().NoError(err)
	s.Require().NoError(conn.Close())
}

func (s *BrokerSuite) TestPing() {
	s.Require().NoError(s.conn.Ping(s.T().Context()))

	conn, err := broker.New(s.T().Context(), s.cfg, broker.WithRetryDelays(testDelays()...))
	s.Require().NoError(err)
	s.Require().NoError(conn.Close())

	s.Require().Error(conn.Ping(s.T().Context()), "закрытое соединение не отвечает")
}

func (s *BrokerSuite) TestPublishConsumeRoundtrip() {
	messages := make(chan broker.Message, 1)

	s.start(func(_ context.Context, msg broker.Message) error {
		messages <- msg

		return nil
	})

	event := s.publish("user-roundtrip")

	select {
	case msg := <-messages:
		s.Equal(broker.RoutingKeyUploaded, msg.Type)
		s.Equal(event.MessageID, msg.MessageID)
		s.Equal(1, msg.Attempt)
		s.False(msg.Final)

		var got broker.AvatarUploadEvent

		s.Require().NoError(json.Unmarshal(msg.Body, &got))
		s.Equal(event, got)
	case <-time.After(waitFor):
		s.Fail("событие не дошло до консьюмера")
	}
}

// TestRetryLadderToDeadLetter проверяет обе ветки классификации разом:
// повторяемая ошибка проходит всю лестницу и заканчивается в очереди мёртвых,
// а неисправимая подтверждается сразу. Одним тестом, потому что «повторов
// не было» доказывается только тем, что за то же время сообщение с повторами
// успело пройти их все.
func (s *BrokerSuite) TestRetryLadderToDeadLetter() {
	dead := s.deadLetters()

	var (
		mu       sync.Mutex
		attempts = map[string][]int{}
	)

	retryable := "user-retryable"
	fatal := "user-fatal"

	s.start(func(_ context.Context, msg broker.Message) error {
		var event broker.AvatarUploadEvent
		if err := json.Unmarshal(msg.Body, &event); err != nil {
			return err
		}

		mu.Lock()
		attempts[event.UserID] = append(attempts[event.UserID], msg.Attempt)
		mu.Unlock()

		switch event.UserID {
		case retryable:
			return errors.New("temporary failure")
		case fatal:
			return fmt.Errorf("broken image: %w", broker.ErrNonRetryable)
		default:
			return nil
		}
	})

	s.publish(fatal)
	retryableEvent := s.publish(retryable)

	select {
	case delivery := <-dead:
		s.Equal(retryableEvent.MessageID, delivery.MessageId)
		// Ключ маршрутизации по дороге переписывается, тип события — нет.
		s.Equal(broker.RoutingKeyUploaded, delivery.Type)
	case <-time.After(waitFor):
		s.Fail("сообщение не дошло до очереди мёртвых")
	}

	mu.Lock()
	defer mu.Unlock()

	s.Equal([]int{1, 2, 3, 4, 5}, attempts[retryable], "попытки должны считаться по заголовку брокера")
	s.Equal([]int{1}, attempts[fatal], "неисправимая ошибка не повторяется")
}

// Отмена контекста прекращает приём новых сообщений, но взятое в работу
// дорабатывается и подтверждается.
func (s *BrokerSuite) TestConsumeFinishesStartedWork() {
	entered := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})

	var once sync.Once

	running := s.start(func(_ context.Context, _ broker.Message) error {
		once.Do(func() { close(entered) })
		<-release
		close(finished)

		return nil
	})

	s.publish("user-shutdown")

	select {
	case <-entered:
	case <-time.After(waitFor):
		s.Fail("обработчик не получил сообщение")

		return
	}

	running.cancel()

	// Отрицательная проверка: пока обработчик не отпущен, Consume обязан ждать.
	select {
	case <-running.done:
		s.Fail("Consume вернулся, не дождавшись начатой обработки")
	case <-time.After(gracePeriod):
	}

	close(release)

	select {
	case <-finished:
	case <-time.After(waitFor):
		s.Fail("обработка не была доведена до конца")
	}

	running.shutdown()

	// Подтверждённое сообщение брокер не возвращает в очередь: следующий
	// консьюмер получает только то, что опубликовано после.
	messages := make(chan broker.Message, 2)

	s.start(func(_ context.Context, msg broker.Message) error {
		messages <- msg

		return nil
	})

	sentinel := s.publish("user-sentinel")

	select {
	case msg := <-messages:
		s.Equal(sentinel.MessageID, msg.MessageID, "неподтверждённое сообщение вернулось бы в очередь первым")
	case <-time.After(waitFor):
		s.Fail("контрольное сообщение не дошло")
	}
}
