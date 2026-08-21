package service

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/kerpe-l/gophprofile/internal/broker"
	"github.com/kerpe-l/gophprofile/internal/domain"
	"github.com/kerpe-l/gophprofile/internal/observability"
)

// UploadInput — загружаемое изображение вместе с тем, что о нём известно
// из запроса.
type UploadInput struct {
	UserID string
	// FileName в ключи хранилища не попадает, только в метаданные.
	FileName string
	Size     int64
	// File перематывается при проверке; закрывает его вызывающий.
	File io.ReadSeeker
}

// Upload проверяет изображение, кладёт оригинал в хранилище и ставит аватар
// в очередь на создание миниатюр.
//
// Порядок операций: запись в базе (uploading) → объект в хранилище →
// перевод в uploaded → событие в брокер. Запись создаётся первой: упавший
// процесс оставляет запись без файла, а не файл без записи.
//
// Неподдерживаемый формат даёт domain.ErrUnsupportedFormat, слишком большое
// изображение — domain.ErrImageTooBig, отказ хранилища переводит запись
// в failed. Отказ публикации загрузку не проваливает: застрявшую запись
// переопубликует reconciler.
func (s *Service) Upload(ctx context.Context, in UploadInput) (domain.Avatar, error) {
	ctx, span := s.tracer.Start(ctx, "service.upload",
		trace.WithAttributes(attribute.String(attrUserID, in.UserID)))
	defer span.End()

	info, err := s.validator.Validate(in.File)
	if err != nil {
		return domain.Avatar{}, observability.SpanError(span,
			fmt.Errorf("validate upload of user %s: %w", in.UserID, err))
	}

	id := uuid.New()
	key := domain.OriginalKey(id)

	span.SetAttributes(attribute.String(attrAvatarID, id.String()))

	avatar, err := s.repo.Create(ctx, domain.NewAvatar{
		ID:        id,
		UserID:    in.UserID,
		FileName:  in.FileName,
		MimeType:  info.MimeType,
		SizeBytes: in.Size,
		Width:     info.Width,
		Height:    info.Height,
		S3Key:     key,
	})
	if err != nil {
		return domain.Avatar{}, observability.SpanError(span,
			fmt.Errorf("upload avatar of user %s: %w", in.UserID, err))
	}

	ctx = context.WithoutCancel(ctx)

	if err := s.storage.Put(ctx, key, in.File, in.Size, info.MimeType); err != nil {
		s.markFailed(ctx, id)

		return domain.Avatar{}, observability.SpanError(span, fmt.Errorf("upload avatar %s: %w", id, err))
	}

	if err := s.repo.SetUploadStatus(ctx, id, domain.UploadStatusUploaded); err != nil {
		s.markFailed(ctx, id)

		return domain.Avatar{}, observability.SpanError(span, fmt.Errorf("upload avatar %s: %w", id, err))
	}

	avatar.UploadStatus = domain.UploadStatusUploaded

	if err := s.publisher.Publish(ctx, broker.NewUploadEvent(id, in.UserID, key)); err != nil {
		// Аватар остаётся uploaded + pending — состояние, которое подберёт
		// reconciler.
		s.log.WarnContext(ctx, "publish upload event",
			slog.Any("error", err), slog.String("avatar_id", id.String()))
	}

	return avatar, nil
}

// markFailed переводит запись в failed после сорвавшейся загрузки.
func (s *Service) markFailed(ctx context.Context, id uuid.UUID) {
	if err := s.repo.SetUploadStatus(ctx, id, domain.UploadStatusFailed); err != nil {
		s.log.ErrorContext(ctx, "mark upload failed",
			slog.Any("error", err), slog.String("avatar_id", id.String()))
	}
}
