// Package service содержит сценарии работы с аватарами: загрузку, раздачу,
// выдачу метаданных и удаление.
//
// Пакет не знает ни про HTTP, ни про SQL, ни про S3 — зависимости приходят
// интерфейсами, объявленными здесь же. Наружу уходят доменные ошибки либо
// обёрнутые ошибки зависимостей; код ответа выбирает транспорт.
package service

import (
	"context"
	"io"
	"iter"
	"log/slog"

	"github.com/google/uuid"

	"github.com/kerpe-l/gophprofile/internal/broker"
	"github.com/kerpe-l/gophprofile/internal/domain"
	"github.com/kerpe-l/gophprofile/internal/imageproc"
	"github.com/kerpe-l/gophprofile/internal/placeholder"
)

// Repository — метаданные аватаров.
type Repository interface {
	// Create создаёт запись об аватаре в состоянии uploading + pending.
	Create(ctx context.Context, in domain.NewAvatar) (domain.Avatar, error)
	// Get возвращает живой аватар по идентификатору либо domain.ErrNotFound.
	Get(ctx context.Context, id uuid.UUID) (domain.Avatar, error)
	// GetCurrent возвращает последний живой аватар пользователя либо
	// domain.ErrNotFound.
	GetCurrent(ctx context.Context, userID string) (domain.Avatar, error)
	// ListByUser перебирает живые аватары пользователя от новых к старым.
	ListByUser(ctx context.Context, userID string) iter.Seq2[domain.Avatar, error]
	SetUploadStatus(ctx context.Context, id uuid.UUID, status domain.UploadStatus) error
	// SoftDelete помечает аватар удалённым; повторное удаление —
	// domain.ErrNotFound.
	SoftDelete(ctx context.Context, id uuid.UUID) error
}

// Storage — оригиналы и миниатюры в объектном хранилище. Файлы штатно убирает
// воркер по событию; DeleteMany — запасной путь на случай отказа публикации.
type Storage interface {
	// Put кладёт объект по ключу, перезаписывая существующий.
	Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
	// Get открывает объект на чтение; отсутствие объекта — domain.ErrNotFound.
	Get(ctx context.Context, key string) (domain.StoredObject, error)
	// DeleteMany удаляет объекты одним запросом, отсутствующие пропуская.
	DeleteMany(ctx context.Context, keys []string) error
}

// Publisher — публикация событий обработки.
type Publisher interface {
	// Publish возвращается после подтверждения брокером.
	Publish(ctx context.Context, event broker.Event) error
}

// Validator — проверка загружаемого изображения.
type Validator interface {
	// Validate проверяет изображение и возвращает его тип и размеры,
	// оставляя поток перемотанным в начало.
	Validate(rs io.ReadSeeker) (imageproc.Info, error)
}

// Service — сценарии работы с аватарами.
type Service struct {
	repo        Repository
	storage     Storage
	publisher   Publisher
	validator   Validator
	placeholder *placeholder.Placeholder
	log         *slog.Logger
}

// New собирает сервис из его зависимостей.
func New(
	repo Repository,
	storage Storage,
	publisher Publisher,
	validator Validator,
	ph *placeholder.Placeholder,
	log *slog.Logger,
) *Service {
	return &Service{
		repo:        repo,
		storage:     storage,
		publisher:   publisher,
		validator:   validator,
		placeholder: ph,
		log:         log,
	}
}
