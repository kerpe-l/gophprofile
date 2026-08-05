//go:build integration

package postgres_test

import (
	"context"
	"database/sql"
	"iter"
	"testing"
	"time"

	"github.com/google/uuid"
	// Драйвер database/sql: миграции и прямые проверки схемы ходят в базу так же,
	// как мигратор, а сам репозиторий работает через pgxpool.
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/suite"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/kerpe-l/gophprofile/internal/config"
	"github.com/kerpe-l/gophprofile/internal/domain"
	"github.com/kerpe-l/gophprofile/internal/migrate"
	"github.com/kerpe-l/gophprofile/internal/repository/postgres"
)

// postgresImage — та же версия, что в docker-compose.yml.
const postgresImage = "postgres:16-alpine"

const (
	userAlice = "alice"
	userBob   = "bob"
)

// RepositorySuite поднимает базу один раз на весь набор: контейнер стартует
// секунды, а тестов много. Изоляция — очисткой таблицы перед каждым тестом.
type RepositorySuite struct {
	suite.Suite

	db   *sql.DB
	repo *postgres.Repository
}

func TestRepository(t *testing.T) {
	t.Parallel()

	suite.Run(t, new(RepositorySuite))
}

func (s *RepositorySuite) SetupSuite() {
	ctx := s.T().Context()

	container, err := tcpostgres.Run(ctx, postgresImage,
		tcpostgres.WithDatabase("gophprofile"),
		tcpostgres.WithUsername("gophprofile"),
		tcpostgres.WithPassword("gophprofile"),
		testcontainers.WithWaitStrategy(
			// Postgres в этом образе стартует дважды: первый раз — чтобы
			// выполнить initdb-скрипты, и только второй готов принимать
			// соединения снаружи.
			wait.ForLog("database system is ready to accept connections").WithOccurrence(2),
		),
	)
	s.Require().NoError(err)
	testcontainers.CleanupContainer(s.T(), container)

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	s.Require().NoError(err)

	s.db, err = sql.Open("pgx", dsn)
	s.Require().NoError(err)
	s.Require().NoError(s.db.PingContext(ctx))

	_, err = migrate.Up(ctx, s.db)
	s.Require().NoError(err)

	s.repo, err = postgres.New(ctx, config.DB{
		DSN:          dsn,
		MaxConns:     4,
		QueryTimeout: 5 * time.Second,
	})
	s.Require().NoError(err)
}

func (s *RepositorySuite) TearDownSuite() {
	if s.repo != nil {
		s.repo.Close()
	}

	if s.db != nil {
		s.Require().NoError(s.db.Close())
	}
}

func (s *RepositorySuite) SetupTest() {
	s.execSQL(`TRUNCATE avatars`)
}

// execSQL выполняет запрос мимо репозитория: так тест готовит состояние,
// которого нельзя добиться его методами, — например заданное updated_at.
func (s *RepositorySuite) execSQL(query string, args ...any) {
	_, err := s.db.ExecContext(s.T().Context(), query, args...)
	s.Require().NoError(err)
}

func (s *RepositorySuite) createAvatar(userID string) domain.Avatar {
	id := uuid.New()

	avatar, err := s.repo.Create(s.T().Context(), domain.NewAvatar{
		ID:        id,
		UserID:    userID,
		FileName:  "photo.png",
		MimeType:  "image/png",
		SizeBytes: 1024,
		Width:     800,
		Height:    600,
		S3Key:     domain.OriginalKey(id),
	})
	s.Require().NoError(err)

	return avatar
}

func (s *RepositorySuite) collect(seq iter.Seq2[domain.Avatar, error]) []domain.Avatar {
	var avatars []domain.Avatar

	for avatar, err := range seq {
		s.Require().NoError(err)

		avatars = append(avatars, avatar)
	}

	return avatars
}

func (s *RepositorySuite) TestCreateReturnsRecordWithDefaults() {
	id := uuid.New()

	created, err := s.repo.Create(s.T().Context(), domain.NewAvatar{
		ID:        id,
		UserID:    userAlice,
		FileName:  "photo.png",
		MimeType:  "image/png",
		SizeBytes: 1024,
		Width:     800,
		Height:    600,
		S3Key:     domain.OriginalKey(id),
	})
	s.Require().NoError(err)

	s.Equal(id, created.ID)
	s.Equal(userAlice, created.UserID)
	s.Equal("photo.png", created.FileName)
	s.Equal("image/png", created.MimeType)
	s.Equal(int64(1024), created.SizeBytes)
	s.Equal(800, created.Width)
	s.Equal(600, created.Height)
	s.Equal(domain.OriginalKey(id), created.S3Key)
	s.Equal(domain.UploadStatusUploading, created.UploadStatus)
	s.Equal(domain.ProcessingStatusPending, created.ProcessingStatus)
	s.Zero(created.RetryCount)
	s.Empty(created.ThumbnailKeys)
	s.False(created.CreatedAt.IsZero())
	s.False(created.UpdatedAt.IsZero())

	got, err := s.repo.Get(s.T().Context(), id)
	s.Require().NoError(err)
	s.Equal(created, got)
}

func (s *RepositorySuite) TestGetUnknownAvatar() {
	_, err := s.repo.Get(s.T().Context(), uuid.New())
	s.Require().ErrorIs(err, domain.ErrNotFound)
}

func (s *RepositorySuite) TestGetCurrentReturnsLatest() {
	older := s.createAvatar(userAlice)
	newer := s.createAvatar(userAlice)
	foreign := s.createAvatar(userBob)

	// Порядок задаётся явно: две вставки подряд могут получить NOW()
	// с разницей в микросекунды, и тест начнёт зависеть от неё.
	s.execSQL(`UPDATE avatars SET created_at = $2 WHERE id = $1`, older.ID, time.Now().Add(-time.Hour))
	s.execSQL(`UPDATE avatars SET created_at = $2 WHERE id = $1`, newer.ID, time.Now().Add(-time.Minute))

	current, err := s.repo.GetCurrent(s.T().Context(), userAlice)
	s.Require().NoError(err)
	s.Equal(newer.ID, current.ID)

	// Чужие аватары в выборку не попадают.
	currentBob, err := s.repo.GetCurrent(s.T().Context(), userBob)
	s.Require().NoError(err)
	s.Equal(foreign.ID, currentBob.ID)

	_, err = s.repo.GetCurrent(s.T().Context(), "nobody")
	s.Require().ErrorIs(err, domain.ErrNotFound)
}

func (s *RepositorySuite) TestListByUserOrdersNewestFirst() {
	first := s.createAvatar(userAlice)
	second := s.createAvatar(userAlice)
	deleted := s.createAvatar(userAlice)
	s.createAvatar(userBob)

	s.execSQL(`UPDATE avatars SET created_at = $2 WHERE id = $1`, first.ID, time.Now().Add(-time.Hour))
	s.execSQL(`UPDATE avatars SET created_at = $2 WHERE id = $1`, second.ID, time.Now().Add(-time.Minute))
	s.Require().NoError(s.repo.SoftDelete(s.T().Context(), deleted.ID))

	avatars := s.collect(s.repo.ListByUser(s.T().Context(), userAlice))

	s.Require().Len(avatars, 2)
	s.Equal(second.ID, avatars[0].ID)
	s.Equal(first.ID, avatars[1].ID)
}

func (s *RepositorySuite) TestListByUserEmpty() {
	s.Empty(s.collect(s.repo.ListByUser(s.T().Context(), "nobody")))
}

// Прерванный перебор не должен ни падать, ни оставлять после себя занятое
// соединение: следующий запрос обязан пройти.
func (s *RepositorySuite) TestListByUserStopsEarly() {
	for range 3 {
		s.createAvatar(userAlice)
	}

	seen := 0

	for range s.repo.ListByUser(s.T().Context(), userAlice) {
		seen++

		break
	}

	s.Equal(1, seen)
	s.Len(s.collect(s.repo.ListByUser(s.T().Context(), userAlice)), 3)
}

func (s *RepositorySuite) TestSetUploadStatus() {
	avatar := s.createAvatar(userAlice)

	s.Require().NoError(s.repo.SetUploadStatus(s.T().Context(), avatar.ID, domain.UploadStatusUploaded))

	got, err := s.repo.Get(s.T().Context(), avatar.ID)
	s.Require().NoError(err)
	s.Equal(domain.UploadStatusUploaded, got.UploadStatus)
	s.True(got.UpdatedAt.After(avatar.UpdatedAt), "updated_at is not moved by the update")

	s.Require().ErrorIs(
		s.repo.SetUploadStatus(s.T().Context(), uuid.New(), domain.UploadStatusFailed),
		domain.ErrNotFound,
	)
}

func (s *RepositorySuite) TestSetProcessingStatus() {
	avatar := s.createAvatar(userAlice)

	s.Require().NoError(s.repo.SetProcessingStatus(s.T().Context(), avatar.ID, domain.ProcessingStatusProcessing))

	got, err := s.repo.Get(s.T().Context(), avatar.ID)
	s.Require().NoError(err)
	s.Equal(domain.ProcessingStatusProcessing, got.ProcessingStatus)

	s.Require().ErrorIs(
		s.repo.SetProcessingStatus(s.T().Context(), uuid.New(), domain.ProcessingStatusFailed),
		domain.ErrNotFound,
	)
}

// Загрузка не откатывается назад, а законченная обработка не начинается
// заново: запрещённый переход обязан отличаться и от успеха, и от отсутствия
// записи — вызывающему по нему решать, повторять работу или нет.
func (s *RepositorySuite) TestForbiddenTransitions() {
	ctx := s.T().Context()

	uploaded := s.createAvatar(userAlice)
	s.Require().NoError(s.repo.SetUploadStatus(ctx, uploaded.ID, domain.UploadStatusUploaded))

	// Назад в uploading и повторно в uploaded — из терминального статуса
	// переходов нет.
	s.Require().ErrorIs(
		s.repo.SetUploadStatus(ctx, uploaded.ID, domain.UploadStatusUploading),
		domain.ErrInvalidTransition,
	)
	s.Require().ErrorIs(
		s.repo.SetUploadStatus(ctx, uploaded.ID, domain.UploadStatusFailed),
		domain.ErrInvalidTransition,
	)

	// Завершить можно только начатую обработку.
	pending := s.createAvatar(userAlice)
	s.Require().ErrorIs(
		s.repo.CompleteProcessing(ctx, pending.ID, nil),
		domain.ErrInvalidTransition,
	)

	s.Require().NoError(s.repo.SetProcessingStatus(ctx, pending.ID, domain.ProcessingStatusProcessing))
	s.Require().NoError(s.repo.CompleteProcessing(ctx, pending.ID, nil))
	s.Require().ErrorIs(
		s.repo.SetProcessingStatus(ctx, pending.ID, domain.ProcessingStatusProcessing),
		domain.ErrInvalidTransition,
	)

	// Статусы записи не изменились ни от одной запрещённой попытки.
	got, err := s.repo.Get(ctx, uploaded.ID)
	s.Require().NoError(err)
	s.Equal(domain.UploadStatusUploaded, got.UploadStatus)
}

// Повторная доставка события застаёт запись уже в processing, и обработка
// начинается заново: иначе после падения обработчика аватар остался бы
// без миниатюр навсегда.
func (s *RepositorySuite) TestProcessingRestarts() {
	ctx := s.T().Context()
	avatar := s.createAvatar(userAlice)

	s.Require().NoError(s.repo.SetProcessingStatus(ctx, avatar.ID, domain.ProcessingStatusProcessing))
	s.Require().NoError(s.repo.SetProcessingStatus(ctx, avatar.ID, domain.ProcessingStatusProcessing))
}

func (s *RepositorySuite) TestCompleteProcessingStoresThumbnails() {
	avatar := s.createAvatar(userAlice)
	s.Require().NoError(
		s.repo.SetProcessingStatus(s.T().Context(), avatar.ID, domain.ProcessingStatusProcessing),
	)

	thumbnails := map[domain.ThumbnailSize]string{
		domain.ThumbnailSmall:  domain.ThumbnailKey(avatar.ID, domain.ThumbnailSmall),
		domain.ThumbnailMedium: domain.ThumbnailKey(avatar.ID, domain.ThumbnailMedium),
	}

	s.Require().NoError(s.repo.CompleteProcessing(s.T().Context(), avatar.ID, thumbnails))

	got, err := s.repo.Get(s.T().Context(), avatar.ID)
	s.Require().NoError(err)
	s.Equal(domain.ProcessingStatusCompleted, got.ProcessingStatus)
	s.Equal(thumbnails, got.ThumbnailKeys)

	key, ok := got.Thumbnail(domain.ThumbnailSmall)
	s.True(ok)
	s.Equal(thumbnails[domain.ThumbnailSmall], key)
}

func (s *RepositorySuite) TestIncrementRetry() {
	avatar := s.createAvatar(userAlice)

	s.Require().NoError(s.repo.IncrementRetry(s.T().Context(), avatar.ID))
	s.Require().NoError(s.repo.IncrementRetry(s.T().Context(), avatar.ID))

	got, err := s.repo.Get(s.T().Context(), avatar.ID)
	s.Require().NoError(err)
	s.Equal(2, got.RetryCount)
}

// Повторное удаление аватара обязано отличаться от первого: клиенту нужен 404,
// а не молчаливое подтверждение удаления того, чего уже нет.
func (s *RepositorySuite) TestSoftDeleteIsIdempotent() {
	avatar := s.createAvatar(userAlice)

	s.Require().NoError(s.repo.SoftDelete(s.T().Context(), avatar.ID))
	s.Require().ErrorIs(s.repo.SoftDelete(s.T().Context(), avatar.ID), domain.ErrNotFound)
}

func (s *RepositorySuite) TestDeletedAvatarIsInvisible() {
	avatar := s.createAvatar(userAlice)
	s.Require().NoError(s.repo.SoftDelete(s.T().Context(), avatar.ID))

	_, err := s.repo.Get(s.T().Context(), avatar.ID)
	s.Require().ErrorIs(err, domain.ErrNotFound)

	_, err = s.repo.GetCurrent(s.T().Context(), userAlice)
	s.Require().ErrorIs(err, domain.ErrNotFound)

	s.Empty(s.collect(s.repo.ListByUser(s.T().Context(), userAlice)))

	// Изменения удалённой записи тоже не проходят: строка отбирается
	// только среди живых.
	s.Require().ErrorIs(
		s.repo.SetProcessingStatus(s.T().Context(), avatar.ID, domain.ProcessingStatusCompleted),
		domain.ErrNotFound,
	)
	s.Require().ErrorIs(s.repo.IncrementRetry(s.T().Context(), avatar.ID), domain.ErrNotFound)
	s.Require().ErrorIs(
		s.repo.CompleteProcessing(s.T().Context(), avatar.ID, nil),
		domain.ErrNotFound,
	)

	// Запись осталась в таблице: удаление мягкое, файлы из хранилища
	// удаляет воркер по событию.
	var count int

	err = s.db.QueryRowContext(s.T().Context(),
		`SELECT count(*) FROM avatars WHERE id = $1 AND deleted_at IS NOT NULL`, avatar.ID).Scan(&count)
	s.Require().NoError(err)
	s.Equal(1, count)
}

func (s *RepositorySuite) TestSelectStuck() {
	cutoff := time.Now().Add(-5 * time.Minute)
	old := cutoff.Add(-time.Minute)
	recent := time.Now()

	stuck := s.createAvatar(userAlice)
	fresh := s.createAvatar(userAlice)
	inProgress := s.createAvatar(userAlice)
	stillUploading := s.createAvatar(userAlice)
	removed := s.createAvatar(userAlice)

	const setState = `UPDATE avatars
		SET upload_status = $2, processing_status = $3, updated_at = $4
		WHERE id = $1`

	s.execSQL(setState, stuck.ID, domain.UploadStatusUploaded, domain.ProcessingStatusPending, old)
	s.execSQL(setState, fresh.ID, domain.UploadStatusUploaded, domain.ProcessingStatusPending, recent)
	s.execSQL(setState, inProgress.ID, domain.UploadStatusUploaded, domain.ProcessingStatusProcessing, old)
	s.execSQL(setState, stillUploading.ID, domain.UploadStatusUploading, domain.ProcessingStatusPending, old)
	s.execSQL(setState, removed.ID, domain.UploadStatusUploaded, domain.ProcessingStatusPending, old)
	s.Require().NoError(s.repo.SoftDelete(s.T().Context(), removed.ID))
	s.execSQL(`UPDATE avatars SET updated_at = $2 WHERE id = $1`, removed.ID, old)

	avatars := s.collect(s.repo.SelectStuck(s.T().Context(), cutoff, 10))

	s.Require().Len(avatars, 1)
	s.Equal(stuck.ID, avatars[0].ID)
}

func (s *RepositorySuite) TestSelectStuckRespectsLimit() {
	cutoff := time.Now().Add(-5 * time.Minute)

	for range 3 {
		avatar := s.createAvatar(userAlice)
		s.execSQL(`UPDATE avatars SET upload_status = $2, updated_at = $3 WHERE id = $1`,
			avatar.ID, domain.UploadStatusUploaded, cutoff.Add(-time.Minute))
	}

	s.Len(s.collect(s.repo.SelectStuck(s.T().Context(), cutoff, 2)), 2)
}

// Неположительный предел — ошибка вызывающего, а не выборка «сколько
// найдётся»: до базы такой перебор не доходит.
func (s *RepositorySuite) TestSelectStuckRejectsNonPositiveLimit() {
	s.createAvatar(userAlice)

	for _, limit := range []int{0, -1} {
		var errs int

		for _, err := range s.repo.SelectStuck(s.T().Context(), time.Now(), limit) {
			s.Require().Error(err)

			errs++
		}

		s.Equalf(1, errs, "предел %d", limit)
	}
}

// Отменённый контекст обязан прерывать запрос ошибкой, а не оставлять
// вызывающего ждать ответа базы.
func (s *RepositorySuite) TestCanceledContext() {
	avatar := s.createAvatar(userAlice)

	ctx, cancel := context.WithCancel(s.T().Context())
	cancel()

	_, err := s.repo.Get(ctx, avatar.ID)
	s.Require().ErrorIs(err, context.Canceled)

	s.Require().ErrorIs(s.repo.IncrementRetry(ctx, avatar.ID), context.Canceled)

	for _, err := range s.repo.ListByUser(ctx, userAlice) {
		s.Require().ErrorIs(err, context.Canceled)
	}
}
