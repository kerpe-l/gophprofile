// Package config загружает конфигурацию из переменных окружения.
//
// Конфиг один на оба бинарника, но проверяется по-разному: LoadServer и
// LoadWorker валидируют только те секции, без которых конкретный бинарник
// работать не может. Обязательные секреты не имеют значений по умолчанию.
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

// Имена переменных окружения.
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
	envHTTPRequestTimeout    = "HTTP_REQUEST_TIMEOUT"

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

	envWorkerProcessTimeout    = "WORKER_PROCESS_TIMEOUT"
	envWorkerReconcileInterval = "WORKER_RECONCILE_INTERVAL"
	envWorkerStuckAfter        = "WORKER_STUCK_AFTER"
	envWorkerReconcileBatch    = "WORKER_RECONCILE_BATCH"
	envWorkerDecodeConcurrency = "WORKER_DECODE_CONCURRENCY"
)

// Окружения, в которых запускается сервис: от них зависит формат логов —
// текст локально, JSON в продакшне.
const (
	EnvLocal      = "local"
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
	// Дедлайн обработки запроса. Загрузка под него не попадает: заливка – ReadTimeout.
	defaultHTTPRequestTimeout = 30 * time.Second

	defaultDBMaxConns     = 10
	defaultDBQueryTimeout = 5 * time.Second

	defaultS3Bucket = "avatars"
	// Регион, который MinIO принимает без настройки.
	defaultS3Region  = "us-east-1"
	defaultS3UseSSL  = false
	defaultS3Timeout = 15 * time.Second

	defaultAMQPPrefetch = 8
	defaultAMQPTimeout  = 10 * time.Second

	defaultImageMaxUploadBytes int64 = 10 << 20 // 10 MB
	// Ограничивает память, необходимую для декодирования.
	defaultImageMaxPixels   int64 = 50_000_000
	defaultImageJPEGQuality int   = 85

	// Обработка сообщения — это чтение оригинала, декодирование до 50 Мпикс,
	// два ресайза и две записи обратно.
	defaultWorkerProcessTimeout = 60 * time.Second
	// За пять минут обработка штатного события успевает начаться и закончиться.
	defaultWorkerReconcileInterval = time.Minute
	defaultWorkerStuckAfter        = 5 * time.Minute
	defaultWorkerReconcileBatch    = 100
	// Пиковую память воркера задаёт это число, а не prefetch.
	defaultWorkerDecodeConcurrency = 2
)

// maxJPEGQuality — верхняя граница качества JPEG в пакете image/jpeg.
const maxJPEGQuality = 100

// Config — конфигурация сервиса целиком. HTTP нужен только серверу,
// Worker — только воркеру; остальные секции общие.
type Config struct {
	App    App
	HTTP   HTTP
	DB     DB
	S3     S3
	AMQP   AMQP
	Image  Image
	Worker Worker
}

// App — общие настройки приложения.
type App struct {
	// Env — EnvLocal или EnvProduction.
	Env       string
	LogLevel  slog.Level
	LogFormat logger.Format
}

// HTTP — параметры HTTP-сервера.
type HTTP struct {
	Addr              string
	ReadHeaderTimeout time.Duration
	// ReadTimeout покрывает чтение всего запроса, включая тело.
	ReadTimeout time.Duration
	// WriteTimeout отсчитывается от конца чтения заголовков, то есть покрывает
	// и чтение тела; для загрузки его продлевает отдельный middleware.
	WriteTimeout    time.Duration
	IdleTimeout     time.Duration
	ShutdownTimeout time.Duration
	// RequestTimeout — предел на обработку запроса, кроме загрузки.
	RequestTimeout time.Duration
}

// DB — доступ к PostgreSQL.
type DB struct {
	// DSN — секрет, значения по умолчанию нет.
	DSN          string
	MaxConns     int
	QueryTimeout time.Duration
}

// S3 — доступ к объектному хранилищу.
type S3 struct {
	// Endpoint — host:port без схемы.
	Endpoint string
	// AccessKey и SecretKey — секреты.
	AccessKey string
	SecretKey string
	Bucket    string
	Region    string
	UseSSL    bool
	Timeout   time.Duration
}

// AMQP — доступ к брокеру.
type AMQP struct {
	// URL содержит учётные данные, поэтому секрет.
	URL      string
	Prefetch int
	// Timeout — предел на публикацию; обработку ограничивает Worker.ProcessTimeout.
	Timeout time.Duration
}

// Image — лимиты и параметры обработки изображений.
type Image struct {
	MaxUploadBytes int64
	// MaxPixels ограничивает изображение до полного декодирования.
	MaxPixels   int64
	JPEGQuality int
}

// Worker — параметры обработчика событий.
type Worker struct {
	// ProcessTimeout — предел на обработку одного сообщения, отдельный
	// от AMQP.Timeout.
	ProcessTimeout    time.Duration
	ReconcileInterval time.Duration
	// StuckAfter — возраст, начиная с которого загрузка без начатой обработки
	// считается зависшей.
	StuckAfter        time.Duration
	ReconcileBatch    int
	DecodeConcurrency int
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
		cfg.Worker.validate(),
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
			RequestTimeout:    r.duration(envHTTPRequestTimeout, defaultHTTPRequestTimeout),
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
		Worker: Worker{
			ProcessTimeout:    r.duration(envWorkerProcessTimeout, defaultWorkerProcessTimeout),
			ReconcileInterval: r.duration(envWorkerReconcileInterval, defaultWorkerReconcileInterval),
			StuckAfter:        r.duration(envWorkerStuckAfter, defaultWorkerStuckAfter),
			ReconcileBatch:    r.integer(envWorkerReconcileBatch, defaultWorkerReconcileBatch),
			DecodeConcurrency: r.integer(envWorkerDecodeConcurrency, defaultWorkerDecodeConcurrency),
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
		positiveDuration(envHTTPRequestTimeout, c.RequestTimeout),
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

func (c Worker) validate() error {
	return errors.Join(
		positiveDuration(envWorkerProcessTimeout, c.ProcessTimeout),
		positiveDuration(envWorkerReconcileInterval, c.ReconcileInterval),
		positiveDuration(envWorkerStuckAfter, c.StuckAfter),
		positive(envWorkerReconcileBatch, int64(c.ReconcileBatch)),
		positive(envWorkerDecodeConcurrency, int64(c.DecodeConcurrency)),
	)
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
