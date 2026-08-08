package s3_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kerpe-l/gophprofile/internal/config"
	"github.com/kerpe-l/gophprofile/internal/domain"
	"github.com/kerpe-l/gophprofile/internal/storage/s3"
)

const (
	testBucket = "avatars"
	testKey    = "originals/97b3d1c8-0000-4000-8000-000000000000"
)

// shortTimeout — предел на операцию в тестах, где важен не результат,
// а то, что вызов вообще возвращается.
const shortTimeout = 200 * time.Millisecond

// recorder — фальшивое хранилище: отвечает на проверку бакета
// и запоминает, какие запросы к нему пришли.
type recorder struct {
	mu       sync.Mutex
	requests []string

	// bucketExists — как отвечать на проверку существования бакета.
	bucketExists bool
	// objects — обработчик всех остальных запросов.
	objects http.HandlerFunc
}

func (rec *recorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Завершающий слэш клиент ставит не везде, поэтому путь нормализуется:
	// «/avatars/» и «/avatars» — один и тот же бакет.
	path := strings.Trim(r.URL.Path, "/")

	rec.mu.Lock()
	rec.requests = append(rec.requests, r.Method+" /"+path)
	rec.mu.Unlock()

	// Проверка бакета: HEAD /bucket. Пакетное удаление идёт на тот же путь,
	// но с query, и сюда попадать не должно.
	if path == testBucket && r.URL.RawQuery == "" {
		if r.Method == http.MethodHead && !rec.bucketExists {
			w.WriteHeader(http.StatusNotFound)

			return
		}

		w.WriteHeader(http.StatusOK)

		return
	}

	rec.objects(w, r)
}

func (rec *recorder) seen() []string {
	rec.mu.Lock()
	defer rec.mu.Unlock()

	return append([]string(nil), rec.requests...)
}

// newStorage поднимает фальшивое хранилище и подключённое к нему Storage.
func newStorage(t *testing.T, rec *recorder, timeout time.Duration) *s3.Storage {
	t.Helper()

	srv := httptest.NewServer(rec)
	t.Cleanup(srv.Close)

	endpoint, err := url.Parse(srv.URL)
	require.NoError(t, err)

	storage, err := s3.New(t.Context(), config.S3{
		Endpoint:  endpoint.Host,
		AccessKey: "access",
		SecretKey: "secret",
		Bucket:    testBucket,
		Region:    "us-east-1",
		Timeout:   timeout,
	})
	require.NoError(t, err)

	t.Cleanup(storage.Close)

	return storage
}

// notFound отвечает на любой запрос к объекту «объекта нет».
func notFound(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusNotFound)
}

// ok отвечает на запись объекта успехом.
func ok(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("ETag", `"d41d8cd98f00b204e9800998ecf8427e"`)
	w.WriteHeader(http.StatusOK)
}

// hang принимает запрос и не отвечает, пока не закроют release.
// Отпускать обработчик обязан сам тест: httptest.Server.Close ждёт возврата
// из обработчиков, и повисший обработчик подвесил бы уборку за собой.
func hang(release <-chan struct{}) http.HandlerFunc {
	return func(_ http.ResponseWriter, _ *http.Request) {
		<-release
	}
}

func TestNewExistingBucket(t *testing.T) {
	t.Parallel()

	rec := &recorder{bucketExists: true, objects: notFound}
	newStorage(t, rec, shortTimeout)

	assert.Equal(t, []string{"HEAD /" + testBucket}, rec.seen(),
		"старт не должен делать с бакетом ничего, кроме проверки")
}

// Бакет заводит инфраструктура: приложение с его отсутствием не мирится
// и не создаёт бакет само.
func TestNewMissingBucket(t *testing.T) {
	t.Parallel()

	rec := &recorder{bucketExists: false, objects: notFound}
	srv := httptest.NewServer(rec)

	t.Cleanup(srv.Close)

	endpoint, err := url.Parse(srv.URL)
	require.NoError(t, err)

	storage, err := s3.New(t.Context(), config.S3{
		Endpoint:  endpoint.Host,
		AccessKey: "access",
		SecretKey: "secret",
		Bucket:    testBucket,
		Region:    "us-east-1",
		Timeout:   shortTimeout,
	})
	require.Error(t, err)
	assert.Nil(t, storage)
	assert.Equal(t, []string{"HEAD /" + testBucket}, rec.seen(), "бакет не должен создаваться приложением")
}

func TestGetMissingObject(t *testing.T) {
	t.Parallel()

	rec := &recorder{bucketExists: true, objects: notFound}
	storage := newStorage(t, rec, shortTimeout)

	// Ошибка обязана вернуться из Get, а не всплыть на первом чтении:
	// к тому моменту статус ответа клиенту уже был бы отправлен.
	object, err := storage.Get(t.Context(), testKey)
	require.ErrorIs(t, err, domain.ErrNotFound)
	assert.Nil(t, object.Body, "закрывать вызывающему нечего")
}

func TestDeleteMissingObject(t *testing.T) {
	t.Parallel()

	const response = `<?xml version="1.0" encoding="UTF-8"?>
<DeleteResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
  <Error><Key>originals/gone</Key><Code>NoSuchKey</Code><Message>Not Found</Message></Error>
</DeleteResult>`

	rec := &recorder{
		bucketExists: true,
		objects: func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/xml")
			_, _ = io.WriteString(w, response)
		},
	}
	storage := newStorage(t, rec, shortTimeout)

	// Удаление идемпотентно: повторная доставка события удаления штатна.
	require.NoError(t, storage.DeleteMany(t.Context(), []string{"originals/gone"}))
}

func TestPutContentType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		give string
		want string
	}{
		{name: "explicit", give: "image/png", want: "image/png"},
		{name: "empty", give: "", want: "application/octet-stream"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			contentTypes := make(chan string, 1)

			rec := &recorder{
				bucketExists: true,
				objects: func(w http.ResponseWriter, r *http.Request) {
					contentTypes <- r.Header.Get("Content-Type")

					ok(w, r)
				},
			}
			storage := newStorage(t, rec, shortTimeout)

			body := "not really an image"
			require.NoError(t, storage.Put(t.Context(), testKey, strings.NewReader(body), int64(len(body)), tc.give))
			assert.Equal(t, tc.want, <-contentTypes)
		})
	}
}

func TestRetryOnTemporaryFailure(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int64

	rec := &recorder{
		bucketExists: true,
		objects: func(w http.ResponseWriter, r *http.Request) {
			if attempts.Add(1) == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)

				return
			}

			ok(w, r)
		},
	}
	// Задержка перед повтором — сотни миллисекунд, поэтому предел здесь
	// заметно больше, чем в остальных тестах.
	storage := newStorage(t, rec, 5*time.Second)

	body := "not really an image"
	require.NoError(t, storage.Put(t.Context(), testKey, strings.NewReader(body), int64(len(body)), "image/png"))
	assert.Equal(t, int64(2), attempts.Load(), "временная ошибка должна была уйти на повтор")
}

func TestDeleteManyPartialFailure(t *testing.T) {
	t.Parallel()

	const response = `<?xml version="1.0" encoding="UTF-8"?>
<DeleteResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
  <Deleted><Key>thumbnails/a/100x100</Key></Deleted>
  <Error><Key>thumbnails/a/300x300</Key><Code>AccessDenied</Code><Message>Access Denied</Message></Error>
</DeleteResult>`

	rec := &recorder{
		bucketExists: true,
		objects: func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/xml")
			_, _ = io.WriteString(w, response)
		},
	}
	storage := newStorage(t, rec, shortTimeout)

	err := storage.DeleteMany(t.Context(), []string{"thumbnails/a/100x100", "thumbnails/a/300x300"})
	require.Error(t, err, "отказ на части ключей не должен выглядеть успехом")
	assert.Contains(t, err.Error(), "thumbnails/a/300x300")
}

func TestDeleteManyEmpty(t *testing.T) {
	t.Parallel()

	rec := &recorder{
		bucketExists: true,
		objects: func(_ http.ResponseWriter, _ *http.Request) {
			assert.Fail(t, "пустой список ключей не должен порождать запрос")
		},
	}
	storage := newStorage(t, rec, shortTimeout)

	require.NoError(t, storage.DeleteMany(t.Context(), nil))
}

// TestHangingStorageOwnTimeout — хранилище приняло соединение и не отвечает.
// Вызов обязан вернуться по собственному пределу хранилища, а не висеть.
func TestHangingStorageOwnTimeout(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		call func(ctx context.Context, storage *s3.Storage) error
	}{
		{
			name: "put",
			call: func(ctx context.Context, storage *s3.Storage) error {
				return storage.Put(ctx, testKey, strings.NewReader("x"), 1, "image/png")
			},
		},
		{
			name: "delete many",
			call: func(ctx context.Context, storage *s3.Storage) error {
				return storage.DeleteMany(ctx, []string{testKey})
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			release := make(chan struct{})
			defer close(release)

			rec := &recorder{bucketExists: true, objects: hang(release)}
			storage := newStorage(t, rec, shortTimeout)

			requireReturns(t, func() error { return tc.call(t.Context(), storage) })
		})
	}
}

// TestHangingStorageCallerDeadline — Get своего предела не ставит: тело
// читается уже после возврата. Ограничивает выдачу дедлайн вызывающего.
func TestHangingStorageCallerDeadline(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	defer close(release)

	rec := &recorder{bucketExists: true, objects: hang(release)}
	storage := newStorage(t, rec, time.Hour)

	requireReturns(t, func() error {
		ctx, cancel := context.WithTimeout(t.Context(), shortTimeout)
		defer cancel()

		object, err := storage.Get(ctx, testKey)
		if err == nil {
			return object.Body.Close()
		}

		return err
	})
}

// requireReturns проверяет, что вызов завершился ошибкой и не завис.
func requireReturns(t *testing.T, call func() error) {
	t.Helper()

	done := make(chan error, 1)

	go func() {
		done <- call()
	}()

	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("вызов к неотвечающему хранилищу не вернулся")
	}
}
