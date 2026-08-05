package postgres

import (
	"context"
	"fmt"
	"iter"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/kerpe-l/gophprofile/internal/domain"
)

// avatarColumns — колонки в том порядке, которого ждёт scanAvatar.
//
// width и height в схеме допускают NULL, но заполняются всегда: размеры
// известны из заголовка изображения ещё при валидации загрузки. COALESCE
// избавляет модель от указателей ради колонок, которые на практике не пусты.
const avatarColumns = `id, user_id, file_name, mime_type, size_bytes,
	COALESCE(width, 0), COALESCE(height, 0), s3_key, thumbnail_s3_keys,
	upload_status, processing_status, retry_count, created_at, updated_at`

// Запросы. Статусы подставляются параметрами, а не пишутся литералами в SQL:
// их значения задаёт домен, и второй копии этих строк быть не должно.
const (
	createAvatarQuery = `
		INSERT INTO avatars (id, user_id, file_name, mime_type, size_bytes, width, height, s3_key)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING ` + avatarColumns

	getAvatarQuery = `
		SELECT ` + avatarColumns + `
		FROM avatars
		WHERE id = $1 AND deleted_at IS NULL`

	// Актуальный аватар пользователя — последний созданный из живых.
	getCurrentAvatarQuery = `
		SELECT ` + avatarColumns + `
		FROM avatars
		WHERE user_id = $1 AND deleted_at IS NULL
		ORDER BY created_at DESC
		LIMIT 1`

	listUserAvatarsQuery = `
		SELECT ` + avatarColumns + `
		FROM avatars
		WHERE user_id = $1 AND deleted_at IS NULL
		ORDER BY created_at DESC`

	// Зависшие загрузки: оригинал в хранилище лежит, а обработка так и не
	// началась — событие в брокер не ушло. Отбор идёт по updated_at, потому
	// что этот момент двигает перевод записи в uploaded.
	selectStuckQuery = `
		SELECT ` + avatarColumns + `
		FROM avatars
		WHERE upload_status = $1 AND processing_status = $2
		  AND deleted_at IS NULL AND updated_at < $3
		ORDER BY updated_at
		LIMIT $4`

	setUploadStatusQuery = `
		UPDATE avatars SET upload_status = $2, updated_at = NOW()
		WHERE id = $1 AND deleted_at IS NULL`

	setProcessingStatusQuery = `
		UPDATE avatars SET processing_status = $2, updated_at = NOW()
		WHERE id = $1 AND deleted_at IS NULL`

	completeProcessingQuery = `
		UPDATE avatars SET thumbnail_s3_keys = $2, processing_status = $3, updated_at = NOW()
		WHERE id = $1 AND deleted_at IS NULL`

	incrementRetryQuery = `
		UPDATE avatars SET retry_count = retry_count + 1, updated_at = NOW()
		WHERE id = $1 AND deleted_at IS NULL`

	softDeleteQuery = `
		UPDATE avatars SET deleted_at = NOW(), updated_at = NOW()
		WHERE id = $1 AND deleted_at IS NULL`
)

// Create создаёт запись об аватаре и возвращает её целиком: статусы,
// счётчик попыток и временные метки проставляет база.
func (r *Repository) Create(ctx context.Context, in domain.NewAvatar) (domain.Avatar, error) {
	avatar, err := r.queryOne(ctx, createAvatarQuery,
		in.ID, in.UserID, in.FileName, in.MimeType, in.SizeBytes, in.Width, in.Height, in.S3Key)
	if err != nil {
		return domain.Avatar{}, fmt.Errorf("create avatar %s: %w", in.ID, err)
	}

	return avatar, nil
}

// Get возвращает аватар по идентификатору.
// Удалённый аватар не возвращается: для него, как и для несуществующего, —
// domain.ErrNotFound.
func (r *Repository) Get(ctx context.Context, id uuid.UUID) (domain.Avatar, error) {
	avatar, err := r.queryOne(ctx, getAvatarQuery, id)
	if err != nil {
		return domain.Avatar{}, fmt.Errorf("get avatar %s: %w", id, err)
	}

	return avatar, nil
}

// GetCurrent возвращает актуальный аватар пользователя — последний созданный
// среди неудалённых. Если живых аватаров нет — domain.ErrNotFound.
func (r *Repository) GetCurrent(ctx context.Context, userID string) (domain.Avatar, error) {
	avatar, err := r.queryOne(ctx, getCurrentAvatarQuery, userID)
	if err != nil {
		return domain.Avatar{}, fmt.Errorf("get current avatar of user %s: %w", userID, err)
	}

	return avatar, nil
}

// ListByUser перебирает живые аватары пользователя от новых к старым.
// Перебор прекращается на первой ошибке; пустая последовательность означает,
// что аватаров нет, — это не ошибка.
func (r *Repository) ListByUser(ctx context.Context, userID string) iter.Seq2[domain.Avatar, error] {
	return r.queryMany(ctx, "list avatars of user "+userID, listUserAvatarsQuery, userID)
}

// SelectStuck перебирает аватары, оригинал которых загружен, а обработка
// не начиналась, и которые не менялись до момента before, — не более limit штук.
//
// Момент отсечки задаёт вызывающий: часы принадлежат ему, иначе такой перебор
// нельзя проверить тестом без ожидания реального времени.
func (r *Repository) SelectStuck(
	ctx context.Context, before time.Time, limit int,
) iter.Seq2[domain.Avatar, error] {
	return r.queryMany(ctx, "select stuck avatars", selectStuckQuery,
		domain.UploadStatusUploaded, domain.ProcessingStatusPending, before, limit)
}

// SetUploadStatus переводит запись в новое состояние загрузки оригинала.
func (r *Repository) SetUploadStatus(ctx context.Context, id uuid.UUID, status domain.UploadStatus) error {
	if err := r.exec(ctx, setUploadStatusQuery, id, status); err != nil {
		return fmt.Errorf("set upload status %s of avatar %s: %w", status, id, err)
	}

	return nil
}

// SetProcessingStatus переводит запись в новое состояние обработки.
func (r *Repository) SetProcessingStatus(ctx context.Context, id uuid.UUID, status domain.ProcessingStatus) error {
	if err := r.exec(ctx, setProcessingStatusQuery, id, status); err != nil {
		return fmt.Errorf("set processing status %s of avatar %s: %w", status, id, err)
	}

	return nil
}

// CompleteProcessing сохраняет ключи готовых миниатюр и завершает обработку.
//
// Ключи и статус пишутся одним запросом: двумя падение между ними оставило бы
// completed без ключей — состояние, в котором раздача миниатюр уже разрешена,
// а отдавать нечего.
func (r *Repository) CompleteProcessing(
	ctx context.Context, id uuid.UUID, thumbnails map[domain.ThumbnailSize]string,
) error {
	err := r.exec(ctx, completeProcessingQuery, id, thumbnails, domain.ProcessingStatusCompleted)
	if err != nil {
		return fmt.Errorf("complete processing of avatar %s: %w", id, err)
	}

	return nil
}

// IncrementRetry увеличивает счётчик попыток обработки.
// Счётчик информационный: решение об отправке в очередь мёртвых сообщений
// принимается по числу попыток из заголовков сообщения, а не по этой колонке.
func (r *Repository) IncrementRetry(ctx context.Context, id uuid.UUID) error {
	if err := r.exec(ctx, incrementRetryQuery, id); err != nil {
		return fmt.Errorf("increment retry count of avatar %s: %w", id, err)
	}

	return nil
}

// SoftDelete помечает аватар удалённым. Повторное удаление даёт
// domain.ErrNotFound: запись отбирается только среди живых.
//
// Владельца метод не сверяет — удаление чужого аватара и удаление
// несуществующего должны различаться вызывающим, а один UPDATE с фильтром
// по владельцу эти случаи не разделяет.
func (r *Repository) SoftDelete(ctx context.Context, id uuid.UUID) error {
	if err := r.exec(ctx, softDeleteQuery, id); err != nil {
		return fmt.Errorf("delete avatar %s: %w", id, err)
	}

	return nil
}

// scanAvatar читает одну строку в модель. Принимает pgx.Row, поэтому годится
// и для одиночного чтения, и для перебора: pgx.Rows его тоже реализует.
func scanAvatar(row pgx.Row) (domain.Avatar, error) {
	var avatar domain.Avatar

	err := row.Scan(
		&avatar.ID,
		&avatar.UserID,
		&avatar.FileName,
		&avatar.MimeType,
		&avatar.SizeBytes,
		&avatar.Width,
		&avatar.Height,
		&avatar.S3Key,
		&avatar.ThumbnailKeys,
		&avatar.UploadStatus,
		&avatar.ProcessingStatus,
		&avatar.RetryCount,
		&avatar.CreatedAt,
		&avatar.UpdatedAt,
	)
	if err != nil {
		return domain.Avatar{}, fmt.Errorf("scan avatar: %w", err)
	}

	return avatar, nil
}
