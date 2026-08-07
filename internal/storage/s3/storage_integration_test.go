//go:build integration

package s3_test

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/stretchr/testify/suite"
	"github.com/testcontainers/testcontainers-go"
	tcminio "github.com/testcontainers/testcontainers-go/modules/minio"

	"github.com/kerpe-l/gophprofile/internal/config"
	"github.com/kerpe-l/gophprofile/internal/domain"
	"github.com/kerpe-l/gophprofile/internal/storage/s3"
)

// minioImage — та же версия, что в docker-compose.yml.
const minioImage = "minio/minio:RELEASE.2025-04-22T22-12-26Z"

const (
	originalKey   = "originals/97b3d1c8-0000-4000-8000-000000000000"
	thumbnailKey  = "thumbnails/97b3d1c8-0000-4000-8000-000000000000/100x100"
	pngMediaType  = "image/png"
	storageBucket = "avatars"
)

// StorageSuite поднимает хранилище один раз на весь набор: контейнер стартует
// секунды, а тестов много. Изоляция — уникальными ключами внутри теста.
type StorageSuite struct {
	suite.Suite

	cfg     config.S3
	storage *s3.Storage
}

func TestStorage(t *testing.T) {
	t.Parallel()

	suite.Run(t, new(StorageSuite))
}

func (s *StorageSuite) SetupSuite() {
	ctx := s.T().Context()

	container, err := tcminio.Run(ctx, minioImage)
	s.Require().NoError(err)
	testcontainers.CleanupContainer(s.T(), container)

	endpoint, err := container.ConnectionString(ctx)
	s.Require().NoError(err)

	s.cfg = config.S3{
		Endpoint:  endpoint,
		AccessKey: container.Username,
		SecretKey: container.Password,
		Bucket:    storageBucket,
		Region:    "us-east-1",
		Timeout:   10 * time.Second,
	}

	// Бакет заводит инфраструктура — в compose это одноразовый minio-init,
	// здесь его роль играет сам тест: хранилище бакетов не создаёт.
	s.makeBucket(ctx, storageBucket)

	s.storage, err = s3.New(ctx, s.cfg)
	s.Require().NoError(err)
}

// makeBucket создаёт бакет мимо проверяемого хранилища.
func (s *StorageSuite) makeBucket(ctx context.Context, bucket string) {
	s.T().Helper()

	client, err := minio.New(s.cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(s.cfg.AccessKey, s.cfg.SecretKey, ""),
		Region: s.cfg.Region,
	})
	s.Require().NoError(err)
	s.Require().NoError(client.MakeBucket(ctx, bucket, minio.MakeBucketOptions{Region: s.cfg.Region}))
}

func (s *StorageSuite) TearDownSuite() {
	if s.storage != nil {
		s.storage.Close()
	}
}

// put кладёт объект и возвращает его содержимое.
func (s *StorageSuite) put(key, contentType string, body []byte) {
	s.T().Helper()

	s.Require().NoError(s.storage.Put(s.T().Context(), key, bytes.NewReader(body), int64(len(body)), contentType))
}

func (s *StorageSuite) TestPutGetRoundtrip() {
	body := []byte("pretend this is a png")
	s.put(originalKey, pngMediaType, body)

	object, err := s.storage.Get(s.T().Context(), originalKey)
	s.Require().NoError(err)

	defer func() {
		s.NoError(object.Body.Close())
	}()

	s.Equal(pngMediaType, object.ContentType)
	s.Equal(int64(len(body)), object.Size)
	s.NotEmpty(object.ETag, "ETag нужен для If-None-Match")

	got, err := io.ReadAll(object.Body)
	s.Require().NoError(err)
	s.Equal(body, got)
}

func (s *StorageSuite) TestPutOverwrites() {
	s.put(originalKey, pngMediaType, []byte("first"))
	s.put(originalKey, pngMediaType, []byte("second"))

	object, err := s.storage.Get(s.T().Context(), originalKey)
	s.Require().NoError(err)

	defer func() {
		s.NoError(object.Body.Close())
	}()

	got, err := io.ReadAll(object.Body)
	s.Require().NoError(err)
	s.Equal([]byte("second"), got)
}

func (s *StorageSuite) TestGetMissing() {
	_, err := s.storage.Get(s.T().Context(), "originals/missing")
	s.Require().ErrorIs(err, domain.ErrNotFound)
}

func (s *StorageSuite) TestDeleteIsIdempotent() {
	s.put(originalKey, pngMediaType, []byte("bytes"))

	ctx := s.T().Context()

	s.Require().NoError(s.storage.Delete(ctx, originalKey))

	_, err := s.storage.Get(ctx, originalKey)
	s.Require().ErrorIs(err, domain.ErrNotFound)

	// Повторная доставка события удаления не должна выглядеть отказом.
	s.Require().NoError(s.storage.Delete(ctx, originalKey))
}

func (s *StorageSuite) TestDeleteManyWithMissingKeys() {
	s.put(originalKey, pngMediaType, []byte("original"))
	s.put(thumbnailKey, pngMediaType, []byte("thumbnail"))

	ctx := s.T().Context()

	// В списке есть и живые ключи, и ключ, которого нет: worker удаляет
	// оригинал вместе с миниатюрами, а миниатюры могли не успеть появиться.
	s.Require().NoError(s.storage.DeleteMany(ctx, []string{originalKey, thumbnailKey, "thumbnails/missing/300x300"}))

	for _, key := range []string{originalKey, thumbnailKey} {
		_, err := s.storage.Get(ctx, key)
		s.Require().ErrorIsf(err, domain.ErrNotFound, "ключ %s", key)
	}
}

// Отсутствующий бакет — дефект развёртывания, а не повод завести его на лету:
// иначе опечатка в имени бакета оборачивается пустым хранилищем без аватаров.
func (s *StorageSuite) TestNewMissingBucket() {
	cfg := s.cfg
	cfg.Bucket = "never-created"

	storage, err := s3.New(s.T().Context(), cfg)
	s.Require().Error(err)
	s.Nil(storage)

	// Бакета так и нет: создание проходит, а на уже созданном хранилищем
	// бакете оно бы отказало.
	s.makeBucket(s.T().Context(), cfg.Bucket)
}

func (s *StorageSuite) TestCancelledContext() {
	ctx, cancel := context.WithCancel(s.T().Context())
	cancel()

	err := s.storage.Put(ctx, originalKey, bytes.NewReader([]byte("bytes")), 5, pngMediaType)
	s.Require().ErrorIs(err, context.Canceled)
}
