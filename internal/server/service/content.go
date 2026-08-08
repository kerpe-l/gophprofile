package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"

	"github.com/kerpe-l/gophprofile/internal/domain"
	"github.com/kerpe-l/gophprofile/internal/placeholder"
)

// Время жизни ответа в кешах. Значение выбирает сервис: только он знает,
// отдаётся ли готовое изображение или временная подмена.
const (
	// cacheReady — оригинал или готовая миниатюра. Содержимое по ключу
	// не меняется: новая загрузка порождает новый аватар.
	cacheReady = 24 * time.Hour
	// cachePending — оригинал вместо ещё не созданной миниатюры. Кеш короткий,
	// иначе клиент не увидит готовую миниатюру до конца суток. На терминальный
	// failed не распространяется: там подмена постоянна.
	cachePending = time.Minute
	// cachePlaceholder — заглушка. Кеш короткий по той же причине: иначе
	// первая загрузка аватара не подхватится теми, кто уже спросил.
	cachePlaceholder = 5 * time.Minute
)

// Content — изображение, готовое к отдаче клиенту, вместе с заголовками,
// которые транспорт обязан выставить.
//
// Тело обязано быть закрыто вызывающим: пока оно открыто, заняты соединение
// с хранилищем и горутина чтения внутри клиента.
type Content struct {
	// Body — содержимое изображения.
	Body io.ReadCloser
	// ContentType — тип содержимого.
	ContentType string
	// ETag — валидатор кеша для If-None-Match, без обрамляющих кавычек.
	ETag string
	// Size — размер тела в байтах.
	Size int64
	// MaxAge — сколько ответу разрешено жить в кешах.
	MaxAge time.Duration
	// IsDefault — отдана заглушка, а не изображение пользователя.
	IsDefault bool
}

// AvatarContent отдаёт изображение конкретного аватара.
//
// Пустой size означает оригинал. Если миниатюра запрошенного размера ещё
// не создана, отдаётся оригинал с коротким кешем: для аватарки временно
// неточный размер лучше, чем отсутствие изображения.
//
// Заглушку метод не отдаёт ни при каком исходе: запрошен объект по
// идентификатору, и его отсутствие — domain.ErrNotFound.
func (s *Service) AvatarContent(ctx context.Context, id uuid.UUID, size domain.ThumbnailSize) (Content, error) {
	avatar, err := s.repo.Get(ctx, id)
	if err != nil {
		return Content{}, fmt.Errorf("get content of avatar %s: %w", id, err)
	}

	content, err := s.content(ctx, avatar, size)
	if err != nil {
		return Content{}, fmt.Errorf("get content of avatar %s: %w", id, err)
	}

	return content, nil
}

// UserAvatarContent отдаёт актуальный аватар пользователя, а при его
// отсутствии — заглушку.
//
// Заглушка подставляется и тогда, когда запись есть, а объекта в хранилище
// нет: незавершённая или провалившаяся загрузка для стороннего клиента
// не отличается от отсутствия аватара, и изображение ему нужно в обоих случаях.
func (s *Service) UserAvatarContent(ctx context.Context, userID string, size domain.ThumbnailSize) (Content, error) {
	avatar, err := s.repo.GetCurrent(ctx, userID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return s.defaultContent(), nil
		}

		return Content{}, fmt.Errorf("get avatar content of user %s: %w", userID, err)
	}

	content, err := s.content(ctx, avatar, size)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return s.defaultContent(), nil
		}

		return Content{}, fmt.Errorf("get avatar content of user %s: %w", userID, err)
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

// defaultContent отдаёт встроенную заглушку. Хранилище в этом не участвует,
// поэтому подстановка работает и при недоступном хранилище.
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
