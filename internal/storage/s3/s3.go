// Package s3 хранит оригиналы аватаров и миниатюры в объектном хранилище
// с S3-совместимым API.
//
// Пакет оперирует ключами, а не идентификаторами аватаров.
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
	// maxRetries задан явно, чтобы не менялся вместе с версией библиотеки.
	maxRetries          = 3
	dialTimeout         = 5 * time.Second
	tlsHandshakeTimeout = 5 * time.Second
	// responseHeaderTimeout — предел на ожидание заголовков ответа: без него
	// зависшее хранилище держит вызов до дедлайна контекста вместо повтора.
	responseHeaderTimeout = 10 * time.Second
	idleConnTimeout       = 90 * time.Second
	maxIdleConnsPerHost   = 16
)

// Storage — доступ к бакету с аватарами.
type Storage struct {
	client    *minio.Client
	bucket    string
	transport *http.Transport
	// timeout — предел на одну операцию.
	timeout time.Duration
}

// New подключается к хранилищу и убеждается, что бакет на месте; отсутствие
// бакета — ошибка старта. Транспорт принадлежит хранилищу и закрывается
// методом Close.
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

	if err := s.checkBucket(ctx); err != nil {
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
	if err := s.checkBucket(ctx); err != nil {
		return fmt.Errorf("ping storage: %w", err)
	}

	return nil
}

// checkBucket убеждается, что бакет существует.
//
// Отсутствие бакета — обычная ошибка, а не domain.ErrNotFound.
func (s *Storage) checkBucket(ctx context.Context) error {
	ctx, cancel := s.withDeadline(ctx)
	defer cancel()

	exists, err := s.client.BucketExists(ctx, s.bucket)
	if err != nil {
		return fmt.Errorf("check bucket %s: %w", s.bucket, err)
	}

	if !exists {
		return fmt.Errorf("bucket %s does not exist", s.bucket)
	}

	return nil
}

// withDeadline — единственное место, где на обращение к хранилищу ставится
// предел времени: его зовут все методы, кроме Get.
func (s *Storage) withDeadline(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, s.timeout)
}

// newTransport задаёт таймауты и размер пула соединений для S3.
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
