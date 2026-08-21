package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/kerpe-l/gophprofile/internal/domain"
	"github.com/kerpe-l/gophprofile/internal/observability"
	"github.com/kerpe-l/gophprofile/internal/placeholder"
)

// Время жизни ответа в кешах.
const (
	// cacheReady — оригинал или готовая миниатюра: содержимое по ключу
	// не меняется, новая загрузка порождает новый аватар.
	cacheReady = 24 * time.Hour
	// cachePending — оригинал вместо ещё не созданной миниатюры; терминальный
	// failed сюда не относится, там подмена постоянна.
	cachePending = time.Minute
	// cachePlaceholder — заглушка: обе подмены временные и живут минуты.
	cachePlaceholder = 5 * time.Minute
)

// Content — изображение, готовое к отдаче клиенту, вместе с заголовками,
// которые транспорт обязан выставить. Тело закрывает вызывающий.
type Content struct {
	Body        io.ReadCloser
	ContentType string
	// ETag — валидатор кеша для If-None-Match, без обрамляющих кавычек.
	ETag string
	Size int64
	// MaxAge — сколько ответу разрешено жить в кешах.
	MaxAge time.Duration
	// IsDefault — отдана заглушка, а не изображение пользователя.
	IsDefault bool
}

// AvatarContent отдаёт изображение конкретного аватара. Пустой size означает
// оригинал; им же подменяется ещё не созданная миниатюра, но с коротким кешем.
// Заглушку метод не отдаёт: отсутствие аватара — domain.ErrNotFound.
func (s *Service) AvatarContent(ctx context.Context, id uuid.UUID, size domain.ThumbnailSize) (Content, error) {
	ctx, span := s.tracer.Start(ctx, "service.avatar_content",
		trace.WithAttributes(attribute.String(attrAvatarID, id.String())))
	defer span.End()

	avatar, err := s.repo.Get(ctx, id)
	if err != nil {
		return Content{}, observability.SpanError(span, fmt.Errorf("get content of avatar %s: %w", id, err))
	}

	content, err := s.content(ctx, avatar, size)
	if err != nil {
		return Content{}, observability.SpanError(span, fmt.Errorf("get content of avatar %s: %w", id, err))
	}

	return content, nil
}

// UserAvatarContent отдаёт актуальный аватар пользователя, а при его
// отсутствии — заглушку. Запись без объекта в хранилище считается
// отсутствием аватара.
func (s *Service) UserAvatarContent(ctx context.Context, userID string, size domain.ThumbnailSize) (Content, error) {
	ctx, span := s.tracer.Start(ctx, "service.user_avatar_content",
		trace.WithAttributes(attribute.String(attrUserID, userID)))
	defer span.End()

	avatar, err := s.repo.GetCurrent(ctx, userID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return s.defaultContent(), nil
		}

		return Content{}, observability.SpanError(span,
			fmt.Errorf("get avatar content of user %s: %w", userID, err))
	}

	content, err := s.content(ctx, avatar, size)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return s.defaultContent(), nil
		}

		return Content{}, observability.SpanError(span,
			fmt.Errorf("get avatar content of user %s: %w", userID, err))
	}

	return content, nil
}

// content открывает изображение аватара в запрошенном размере.
func (s *Service) content(ctx context.Context, avatar domain.Avatar, size domain.ThumbnailSize) (Content, error) {
	key, maxAge := avatar.S3Key, cacheReady

	if size != "" {
		if thumbnail, ok := avatar.Thumbnail(size); ok {
			key = thumbnail
		} else if avatar.ProcessingStatus != domain.ProcessingStatusFailed {
			maxAge = cachePending
		}
	}

	object, err := s.storage.Get(ctx, key)
	if err != nil {
		return Content{}, err
	}

	return Content{
		Body:        object.Body,
		ContentType: object.ContentType,
		ETag:        object.ETag,
		Size:        object.Size,
		MaxAge:      maxAge,
	}, nil
}

// defaultContent отдаёт встроенную заглушку; хранилище в этом не участвует.
func (s *Service) defaultContent() Content {
	return Content{
		Body:        io.NopCloser(s.placeholder.Reader()),
		ContentType: placeholder.ContentType,
		ETag:        s.placeholder.ETag(),
		Size:        s.placeholder.Size(),
		MaxAge:      cachePlaceholder,
		IsDefault:   true,
	}
}
