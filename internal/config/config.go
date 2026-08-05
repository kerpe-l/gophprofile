// Package config загружает конфигурацию из переменных окружения.
//
// Конфиг один на оба бинарника, но проверяется по-разному: LoadServer и
// LoadWorker валидируют только те секции, без которых конкретный бинарник
// работать не может. У секретов нет значений по умолчанию — незаданный
// секрет валит старт, а не уезжает в продакшн с дефолтом.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/kerpe-l/gophprofile/internal/logger"
)

// Имена переменных окружения. Литералы, разбросанные по файлам, при
// переименовании не вызывают ошибки компиляции — конфиг просто тихо
// перестаёт читать переменную.
const (
	envAppEnv    = "APP_ENV"
	envLogLevel  = "LOG_LEVEL"
	envLogFormat = "LOG_FORMAT"

	envHTTPAddr              = "HTTP_ADDR"
	envHTTPReadHeaderTimeout = "HTTP_READ_HEADER_TIMEOUT"
	envHTTPReadTimeout       = "HTTP_READ_TIMEOUT"
	envHTTPWriteTimeout      = "HTTP_WRITE_TIMEOUT" //nolint:gosec // имя переменной окружения, а не значение секрета
	envHTTPIdleTimeout       = "HTTP_IDLE_TIMEOUT"
	envHTTPShutdownTimeout   = "HTTP_SHUTDOWN_TIMEOUT"

	envDatabaseDSN    = "DATABASE_DSN"
	envDBMaxConns     = "DB_MAX_CONNS"
	envDBQueryTimeout = "DB_QUERY_TIMEOUT"

	envS3Endpoint  = "S3_ENDPOINT"
	envS3AccessKey = "S3_ACCESS_KEY"
	envS3SecretKey = "S3_SECRET_KEY" //nolint:gosec // имя переменной окружения, а не значение секрета
	envS3Bucket    = "S3_BUCKET"
	envS3Region    = "S3_REGION"
	envS3UseSSL    = "S3_USE_SSL"
	envS3Timeout   = "S3_TIMEOUT"

	envAMQPURL      = "AMQP_URL"
	envAMQPPrefetch = "AMQP_PREFETCH"
	envAMQPTimeout  = "AMQP_TIMEOUT"

	envImageMaxUploadBytes = "IMAGE_MAX_UPLOAD_BYTES"
	envImageMaxPixels      = "IMAGE_MAX_PIXELS"
	envImageJPEGQuality    = "IMAGE_JPEG_QUALITY"
)

// Окружения, в которых запускается сервис.
const (
	// EnvLocal — локальный запуск: логи в текстовом формате.
	EnvLocal = "local"
	// EnvProduction — продакшн: логи в JSON.
	EnvProduction = "production"
)

// Значения по умолчанию.
const (
	defaultHTTPAddr = ":8080"
	// Заливка 10 MB по медленному мобильному каналу занимает минуты,
	// поэтому ReadTimeout здесь заметно больше остальных.
	defaultHTTPReadTimeout       = 3 * time.Minute
	defaultHTTPReadHeaderTimeout = 5 * time.Second
	defaultHTTPWriteTimeout      = 60 * time.Second
	defaultHTTPIdleTimeout       = 2 * time.Minute
	defaultHTTPShutdownTimeout   = 15 * time.Second

	defaultDBMaxConns     = 10
	defaultDBQueryTimeout = 5 * time.Second

	defaultS3Bucket = "avatars"
	// Регион по умолчанию — тот, который MinIO принимает без настройки;
	defaultS3Region  = "us-east-1"
	defaultS3UseSSL  = false
	defaultS3Timeout = 15 * time.Second

	defaultAMQPPrefetch = 8
	defaultAMQPTimeout  = 10 * time.Second

	defaultImageMaxUploadBytes int64 = 10 << 20 // 10 MB
	// 10 MB PNG разворачивается в 30000×30000 и кладёт процесс по памяти
	// раньше, чем сработает лимит на размер файла, — отсюда отдельный лимит
	// на число пикселей.
	defaultImageMaxPixels   int64 = 50_000_000
	defaultImageJPEGQuality int   = 85
)

// maxJPEGQuality — верхняя граница качества JPEG в пакете image/jpeg.
const maxJPEGQuality = 100

// Config — конфигурация сервиса целиком.
type Config struct {
	// App — общие настройки приложения и логирования.
	App App
	// HTTP — параметры HTTP-сервера; нужны только серверу.
	HTTP HTTP
	// DB — доступ к PostgreSQL.
	DB DB
	// S3 — доступ к объектному хранилищу.
	S3 S3
	// AMQP — доступ к брокеру.
	AMQP AMQP
	// Image — лимиты и параметры обработки изображений.
	Image Image
}

// App — общие настройки приложения.
type App struct {
	// Env — окружение: EnvLocal или EnvProduction.
	Env string
	// LogLevel — минимальный уровень записей в логе.
	LogLevel slog.Level
	// LogFormat — формат вывода логов.
	LogFormat logger.Format
}

// HTTP — параметры HTTP-сервера.
type HTTP struct {
	// Addr — адрес прослушивания в формате host:port.
	Addr string
	// ReadHeaderTimeout — предел на чтение заголовков запроса.
	ReadHeaderTimeout time.Duration
	// ReadTimeout — предел на чтение всего запроса, включая тело.
	ReadTimeout time.Duration
	// WriteTimeout — предел на запись ответа.
	WriteTimeout time.Duration
	// IdleTimeout — предел простоя keep-alive соединения.
	IdleTimeout time.Duration
	// ShutdownTimeout — сколько ждать завершения активных запросов при остановке.
	ShutdownTimeout time.Duration
}

// DB — доступ к PostgreSQL.
type DB struct {
	// DSN — строка подключения; секрет, значения по умолчанию нет.
	DSN string
	// MaxConns — верхняя граница пула соединений.
	MaxConns int
	// QueryTimeout — предел на один запрос к БД.
	QueryTimeout time.Duration
}

// S3 — доступ к объектному хранилищу.
type S3 struct {
	// Endpoint — адрес хранилища в формате host:port, без схемы.
	Endpoint string
	// AccessKey — идентификатор ключа доступа; секрет.
	AccessKey string
	// SecretKey — секретный ключ доступа; секрет.
	SecretKey string
	// Bucket — бакет для оригиналов и миниатюр.
	Bucket string
	// Region — регион хранилища; участвует в подписи запросов.
	Region string
	// UseSSL — обращаться к хранилищу по HTTPS.
	UseSSL bool
	// Timeout — предел на одну операцию с хранилищем.
	Timeout time.Duration
}

// AMQP — доступ к брокеру.
type AMQP struct {
	// URL — строка подключения; содержит учётные данные, поэтому секрет.
	URL string
	// Prefetch — сколько сообщений консьюмер берёт не подтверждая.
	Prefetch int
	// Timeout — предел на публикацию и на обработку одного сообщения.
	Timeout time.Duration
}

// Image — лимиты и параметры обработки изображений.
type Image struct {
	// MaxUploadBytes — предельный размер загружаемого файла.
	MaxUploadBytes int64
	// MaxPixels — предельное число пикселей (ширина·высота) изображения.
	MaxPixels int64
	// JPEGQuality — качество JPEG, с которым сохраняются миниатюры.
	JPEGQuality int
}

// getenv — источник значений переменных окружения.
// Отдельный тип нужен, чтобы тесты не правили окружение процесса.
type getenv func(key string) string

// LoadServer загружает конфигурацию API-сервера и проверяет все секции.
func LoadServer() (*Config, error) {
	return loadServer(os.Getenv)
}

// LoadWorker загружает конфигурацию обработчика событий.
// Секция HTTP не проверяется: воркер не слушает HTTP.
func LoadWorker() (*Config, error) {
	return loadWorker(os.Getenv)
}

// LoadMigrator загружает конфигурацию мигратора.
// Проверяются только App и DB: мигратор применяет схему и выходит, ни
// хранилища, ни брокера, ни HTTP-порта ему не нужно.
func LoadMigrator() (*Config, error) {
	return loadMigrator(os.Getenv)
}

func loadServer(env getenv) (*Config, error) {
	cfg, err := load(env)
	if err != nil {
		return nil, err
	}

	err = errors.Join(
		cfg.App.validate(),
		cfg.HTTP.validate(),
		cfg.DB.validate(),
		cfg.S3.validate(),
		cfg.AMQP.validate(),
		cfg.Image.validate(),
	)
	if err != nil {
		return nil, fmt.Errorf("invalid server configuration: %w", err)
	}

	return cfg, nil
}

func loadWorker(env getenv) (*Config, error) {
	cfg, err := load(env)
	if err != nil {
		return nil, err
	}

	err = errors.Join(
		cfg.App.validate(),
		cfg.DB.validate(),
		cfg.S3.validate(),
		cfg.AMQP.validate(),
		cfg.Image.validate(),
	)
	if err != nil {
		return nil, fmt.Errorf("invalid worker configuration: %w", err)
	}

	return cfg, nil
}

func loadMigrator(env getenv) (*Config, error) {
	cfg, err := load(env)
	if err != nil {
		return nil, err
	}

	err = errors.Join(
		cfg.App.validate(),
		cfg.DB.validate(),
	)
	if err != nil {
		return nil, fmt.Errorf("invalid migrator configuration: %w", err)
	}

	return cfg, nil
}

// load читает все секции. Ошибки разбора значений копятся и возвращаются разом.
func load(env getenv) (*Config, error) {
	r := reader{env: env}

	appEnv := r.str(envAppEnv, EnvLocal)

	cfg := &Config{
		App: App{
			Env:       appEnv,
			LogLevel:  r.level(envLogLevel, slog.LevelInfo),
			LogFormat: r.format(envLogFormat, defaultLogFormat(appEnv)),
		},
		HTTP: HTTP{
			Addr:              r.str(envHTTPAddr, defaultHTTPAddr),
			ReadHeaderTimeout: r.duration(envHTTPReadHeaderTimeout, defaultHTTPReadHeaderTimeout),
			ReadTimeout:       r.duration(envHTTPReadTimeout, defaultHTTPReadTimeout),
			WriteTimeout:      r.duration(envHTTPWriteTimeout, defaultHTTPWriteTimeout),
			IdleTimeout:       r.duration(envHTTPIdleTimeout, defaultHTTPIdleTimeout),
			ShutdownTimeout:   r.duration(envHTTPShutdownTimeout, defaultHTTPShutdownTimeout),
		},
		DB: DB{
			DSN:          r.str(envDatabaseDSN, ""),
			MaxConns:     r.integer(envDBMaxConns, defaultDBMaxConns),
			QueryTimeout: r.duration(envDBQueryTimeout, defaultDBQueryTimeout),
		},
		S3: S3{
			Endpoint:  r.str(envS3Endpoint, ""),
			AccessKey: r.str(envS3AccessKey, ""),
			SecretKey: r.str(envS3SecretKey, ""),
			Bucket:    r.str(envS3Bucket, defaultS3Bucket),
			Region:    r.str(envS3Region, defaultS3Region),
			UseSSL:    r.boolean(envS3UseSSL, defaultS3UseSSL),
			Timeout:   r.duration(envS3Timeout, defaultS3Timeout),
		},
		AMQP: AMQP{
			URL:      r.str(envAMQPURL, ""),
			Prefetch: r.integer(envAMQPPrefetch, defaultAMQPPrefetch),
			Timeout:  r.duration(envAMQPTimeout, defaultAMQPTimeout),
		},
		Image: Image{
			MaxUploadBytes: r.integer64(envImageMaxUploadBytes, defaultImageMaxUploadBytes),
			MaxPixels:      r.integer64(envImageMaxPixels, defaultImageMaxPixels),
			JPEGQuality:    r.integer(envImageJPEGQuality, defaultImageJPEGQuality),
		},
	}

	if err := errors.Join(r.errs...); err != nil {
		return nil, fmt.Errorf("read environment: %w", err)
	}

	return cfg, nil
}

// defaultLogFormat выбирает формат логов по окружению: текст локально,
// JSON везде остальном.
func defaultLogFormat(appEnv string) logger.Format {
	if appEnv == EnvLocal {
		return logger.FormatText
	}

	return logger.FormatJSON
}

func (c App) validate() error {
	switch c.Env {
	case EnvLocal, EnvProduction:
		return nil
	default:
		return fmt.Errorf("%s must be one of %q, %q", envAppEnv, EnvLocal, EnvProduction)
	}
}

func (c HTTP) validate() error {
	return errors.Join(
		required(envHTTPAddr, c.Addr),
		positiveDuration(envHTTPReadHeaderTimeout, c.ReadHeaderTimeout),
		positiveDuration(envHTTPReadTimeout, c.ReadTimeout),
		positiveDuration(envHTTPWriteTimeout, c.WriteTimeout),
		positiveDuration(envHTTPIdleTimeout, c.IdleTimeout),
		positiveDuration(envHTTPShutdownTimeout, c.ShutdownTimeout),
	)
}

func (c DB) validate() error {
	return errors.Join(
		required(envDatabaseDSN, c.DSN),
		positive(envDBMaxConns, int64(c.MaxConns)),
		positiveDuration(envDBQueryTimeout, c.QueryTimeout),
	)
}

func (c S3) validate() error {
	return errors.Join(
		required(envS3Endpoint, c.Endpoint),
		required(envS3AccessKey, c.AccessKey),
		required(envS3SecretKey, c.SecretKey),
		required(envS3Bucket, c.Bucket),
		required(envS3Region, c.Region),
		positiveDuration(envS3Timeout, c.Timeout),
	)
}

func (c AMQP) validate() error {
	return errors.Join(
		required(envAMQPURL, c.URL),
		positive(envAMQPPrefetch, int64(c.Prefetch)),
		positiveDuration(envAMQPTimeout, c.Timeout),
	)
}

func (c Image) validate() error {
	err := errors.Join(
		positive(envImageMaxUploadBytes, c.MaxUploadBytes),
		positive(envImageMaxPixels, c.MaxPixels),
		positive(envImageJPEGQuality, int64(c.JPEGQuality)),
	)
	if c.JPEGQuality > maxJPEGQuality {
		err = errors.Join(err, fmt.Errorf("%s must be at most %d", envImageJPEGQuality, maxJPEGQuality))
	}

	return err
}

func required(key, value string) error {
	if value == "" {
		return fmt.Errorf("%s is required", key)
	}

	return nil
}

func positive(key string, value int64) error {
	if value <= 0 {
		return fmt.Errorf("%s must be positive", key)
	}

	return nil
}

func positiveDuration(key string, value time.Duration) error {
	if value <= 0 {
		return fmt.Errorf("%s must be positive", key)
	}

	return nil
}

// reader читает переменные окружения, накапливая ошибки разбора.
type reader struct {
	env  getenv
	errs []error
}

func (r *reader) str(key, def string) string {
	if v := r.env(key); v != "" {
		return v
	}

	return def
}

func (r *reader) integer(key string, def int) int {
	v := r.env(key)
	if v == "" {
		return def
	}

	n, err := strconv.Atoi(v)
	if err != nil {
		r.errs = append(r.errs, fmt.Errorf("%s: %w", key, err))

		return def
	}

	return n
}

func (r *reader) integer64(key string, def int64) int64 {
	v := r.env(key)
	if v == "" {
		return def
	}

	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		r.errs = append(r.errs, fmt.Errorf("%s: %w", key, err))

		return def
	}

	return n
}

func (r *reader) boolean(key string, def bool) bool {
	v := r.env(key)
	if v == "" {
		return def
	}

	b, err := strconv.ParseBool(v)
	if err != nil {
		r.errs = append(r.errs, fmt.Errorf("%s: %w", key, err))

		return def
	}

	return b
}

func (r *reader) duration(key string, def time.Duration) time.Duration {
	v := r.env(key)
	if v == "" {
		return def
	}

	d, err := time.ParseDuration(v)
	if err != nil {
		r.errs = append(r.errs, fmt.Errorf("%s: %w", key, err))

		return def
	}

	return d
}

func (r *reader) level(key string, def slog.Level) slog.Level {
	v := r.env(key)
	if v == "" {
		return def
	}

	var l slog.Level
	if err := l.UnmarshalText([]byte(v)); err != nil {
		r.errs = append(r.errs, fmt.Errorf("%s: %w", key, err))

		return def
	}

	return l
}

func (r *reader) format(key string, def logger.Format) logger.Format {
	v := r.env(key)
	if v == "" {
		return def
	}

	switch f := logger.Format(strings.ToLower(v)); f {
	case logger.FormatJSON, logger.FormatText:
		return f
	default:
		r.errs = append(r.errs, fmt.Errorf("%s must be one of %q, %q", key, logger.FormatJSON, logger.FormatText))

		return def
	}
}
