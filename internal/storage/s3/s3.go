// Package s3 хранит оригиналы аватаров и миниатюры в объектном хранилище
// с S3-совместимым API.
//
// Пакет оперирует ключами, а не идентификаторами аватаров: где лежит оригинал
// и где миниатюра, решает домен. Отсутствие объекта наружу выглядит как
// domain.ErrNotFound — так же, как отсутствие записи в базе.
//
// Объект, который вернул Get, обязан быть закрыт вызывающим: до Close заняты
// и соединение, и горутина чтения внутри клиента.
package s3

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/kerpe-l/gophprofile/internal/config"
	"github.com/kerpe-l/gophprofile/internal/domain"
)

// Параметры клиента, не выведенные в конфиг.
const (
	// maxRetries — сколько раз клиент повторяет временную ошибку. Значение
	// задаётся явно, чтобы поведение не менялось вместе с версией библиотеки.
	maxRetries = 3
	// dialTimeout — предел на установку TCP-соединения с хранилищем.
	dialTimeout = 5 * time.Second
	// tlsHandshakeTimeout — предел на TLS-хендшейк.
	tlsHandshakeTimeout = 5 * time.Second
	// responseHeaderTimeout — сколько ждать заголовков ответа после отправки
	// запроса. Без него зависшее хранилище держит вызов до дедлайна контекста,
	// вместо того чтобы отвалиться и уйти на повтор.
	responseHeaderTimeout = 10 * time.Second
	// idleConnTimeout — сколько живёт простаивающее соединение в пуле.
	idleConnTimeout = 90 * time.Second
	// maxIdleConnsPerHost — размер пула на хост; хост здесь ровно один.
	maxIdleConnsPerHost = 16
)

// Storage — доступ к бакету с аватарами.
type Storage struct {
	client *minio.Client
	bucket string
	// transport принадлежит хранилищу: его простаивающие соединения
	// закрывает Close.
	transport *http.Transport
	// timeout — предел на одну операцию. Поле, а не константа пакета, чтобы
	// тест мог подставить своё значение и не ждать реальных секунд.
	timeout time.Duration
}

// New подключается к хранилищу и убеждается, что бакет существует, создавая
// его при необходимости. Транспорт принадлежит хранилищу: закрывается методом
// Close и только им.
func New(ctx context.Context, cfg config.S3) (*Storage, error) {
	transport := newTransport()

	client, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
		// Заданный регион избавляет от отдельного запроса за расположением
		// бакета перед первой операцией.
		Region:     cfg.Region,
		Transport:  transport,
		MaxRetries: maxRetries,
	})
	if err != nil {
		return nil, fmt.Errorf("create storage client: %w", err)
	}

	s := &Storage{
		client:    client,
		bucket:    cfg.Bucket,
		transport: transport,
		timeout:   cfg.Timeout,
	}

	if err := s.ensureBucket(ctx); err != nil {
		s.Close()

		return nil, err
	}

	return s, nil
}

// Close закрывает простаивающие соединения с хранилищем.
// Повторный вызов безопасен.
func (s *Storage) Close() {
	s.transport.CloseIdleConnections()
}

// Ping проверяет, что хранилище отвечает и бакет на месте.
func (s *Storage) Ping(ctx context.Context) error {
	ctx, cancel := s.withDeadline(ctx)
	defer cancel()

	if _, err := s.client.BucketExists(ctx, s.bucket); err != nil {
		return fmt.Errorf("ping storage: %w", err)
	}

	return nil
}

// ensureBucket создаёт бакет, если его ещё нет. Гонка двух стартующих
// процессов безопасна: хранилище отвечает на повторное создание кодом
// BucketAlreadyOwnedByYou, и это не ошибка.
func (s *Storage) ensureBucket(ctx context.Context) error {
	ctx, cancel := s.withDeadline(ctx)
	defer cancel()

	exists, err := s.client.BucketExists(ctx, s.bucket)
	if err != nil {
		return fmt.Errorf("check bucket: %w", err)
	}

	if exists {
		return nil
	}

	if err := s.client.MakeBucket(ctx, s.bucket, minio.MakeBucketOptions{}); err != nil {
		if minio.ToErrorResponse(err).Code == minio.BucketAlreadyOwnedByYou {
			return nil
		}

		return fmt.Errorf("create bucket: %w", err)
	}

	return nil
}

// withDeadline — единственное место, где на обращение к хранилищу ставится
// предел времени: его зовут все методы, кроме Get, и новый метод не может
// о нём забыть. Дедлайн вызывающего не перебивается — тот, кто выше по стеку,
// знает про общий бюджет операции больше.
func (s *Storage) withDeadline(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return context.WithCancel(ctx)
	}

	return context.WithTimeout(ctx, s.timeout)
}

// newTransport собирает транспорт с пределами на каждую фазу запроса.
// У http.DefaultTransport нет ни предела на TLS-хендшейк, ни предела
// на ожидание заголовков ответа.
func newTransport() *http.Transport {
	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: dialTimeout}).DialContext,
		TLSHandshakeTimeout:   tlsHandshakeTimeout,
		ResponseHeaderTimeout: responseHeaderTimeout,
		IdleConnTimeout:       idleConnTimeout,
		MaxIdleConnsPerHost:   maxIdleConnsPerHost,
	}
}

// mapError переводит ошибку клиента в доменную: отсутствие объекта или бакета
// неотличимо от отсутствия записи. Остальные ошибки оборачиваются с ключом —
// без ключа по логу не найти, о каком объекте речь.
func mapError(what, key string, err error) error {
	if err == nil {
		return nil
	}

	if isNotFound(err) {
		return fmt.Errorf("%s %s: %w", what, key, domain.ErrNotFound)
	}

	return fmt.Errorf("%s %s: %w", what, key, err)
}

// isNotFound распознаёт ответ хранилища «объекта нет». Статус проверяется
// наравне с кодом: коды у S3-совместимых реализаций расходятся, а 404 отдают
// все.
func isNotFound(err error) bool {
	var resp minio.ErrorResponse
	if !errors.As(err, &resp) {
		return false
	}

	switch resp.Code {
	case minio.NoSuchKey, minio.NoSuchBucket:
		return true
	}

	return resp.StatusCode == http.StatusNotFound
}
