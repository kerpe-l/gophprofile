package service

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"github.com/google/uuid"

	"github.com/kerpe-l/gophprofile/internal/broker"
	"github.com/kerpe-l/gophprofile/internal/domain"
)

// UploadInput — загружаемое изображение вместе с тем, что о нём известно
// из запроса.
type UploadInput struct {
	// UserID — владелец будущего аватара.
	UserID string
	// FileName — имя файла, под которым изображение загрузили. В ключи
	// хранилища не попадает и хранится только ради метаданных.
	FileName string
	// Size — размер файла в байтах.
	Size int64
	// File — содержимое файла. Проверка перематывает поток, поэтому он
	// обязан уметь Seek; закрывает файл вызывающий.
	File io.ReadSeeker
}

// Upload проверяет изображение, кладёт оригинал в хранилище и ставит аватар
// в очередь на создание миниатюр.
//
// Порядок операций: запись в базе (uploading) → объект в хранилище →
// перевод в uploaded → событие в брокер. Атомарности между тремя системами
// нет, поэтому запись создаётся первой: упавший после неё процесс оставляет
// запись без файла, а не файл, на который никто не сошлётся.
//
// Неподдерживаемый формат даёт domain.ErrUnsupportedFormat, слишком большое
// изображение — domain.ErrImageTooBig. Отказ хранилища переводит запись
// в failed и возвращается вызывающему. Отказ публикации загрузку не проваливает:
// оригинал уже на месте, и застрявшую запись переопубликует reconciler.
func (s *Service) Upload(ctx context.Context, in UploadInput) (domain.Avatar, error) {
	info, err := s.validator.Validate(in.File)
	if err != nil {
		return domain.Avatar{}, fmt.Errorf("validate upload of user %s: %w", in.UserID, err)
	}

	id := uuid.New()
	key := domain.OriginalKey(id)

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
		return domain.Avatar{}, fmt.Errorf("upload avatar of user %s: %w", in.UserID, err)
	}

	// Клиент, отвалившийся сразу после заливки файла, отменяет контекст
	// запроса. Работа к этому моменту сделана, и записать её результат нужно
	// независимо от того, дождался ли он ответа: запись, оставшаяся в uploading
	// с уже лежащим в хранилище файлом, не подбирается ничем. Предел времени
	// на эти вызовы ставят репозиторий, хранилище и публикатор.
	ctx = context.WithoutCancel(ctx)

	if err := s.storage.Put(ctx, key, in.File, in.Size, info.MimeType); err != nil {
		s.markFailed(ctx, id)

		return domain.Avatar{}, fmt.Errorf("upload avatar %s: %w", id, err)
	}

	if err := s.repo.SetUploadStatus(ctx, id, domain.UploadStatusUploaded); err != nil {
		return domain.Avatar{}, fmt.Errorf("upload avatar %s: %w", id, err)
	}

	avatar.UploadStatus = domain.UploadStatusUploaded

	if err := s.publisher.Publish(ctx, broker.NewUploadEvent(id, in.UserID, key)); err != nil {
		// Аватар остаётся uploaded + pending — ровно то состояние, которое
		// reconciler переопубликует. Заставлять клиента заливать файл заново
		// ради записи, которая и так будет обработана, незачем.
		s.log.WarnContext(ctx, "publish upload event",
			slog.Any("error", err), slog.String("avatar_id", id.String()))
	}

	return avatar, nil
}

// markFailed переводит запись в failed после неудачной загрузки оригинала.
//
// Ошибку перевода возвращать некому: наружу уходит отказ хранилища, ради
// которого сюда и пришли, и подменять его причиной второго порядка значило бы
// потерять исходную. Запись остаётся в uploading без файла — состояние,
// которое ничего не отдаёт клиенту и никем не подбирается.
func (s *Service) markFailed(ctx context.Context, id uuid.UUID) {
	if err := s.repo.SetUploadStatus(ctx, id, domain.UploadStatusFailed); err != nil {
		s.log.ErrorContext(ctx, "mark upload failed",
			slog.Any("error", err), slog.String("avatar_id", id.String()))
	}
}
