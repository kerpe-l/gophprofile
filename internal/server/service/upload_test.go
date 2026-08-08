package service_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kerpe-l/gophprofile/internal/broker"
	"github.com/kerpe-l/gophprofile/internal/domain"
	"github.com/kerpe-l/gophprofile/internal/server/service"
)

// errStorage — отказ инфраструктуры, ничем не похожий на доменную ошибку.
var errStorage = errors.New("storage is unavailable")

const testUserID = "user-42"

func uploadInput() service.UploadInput {
	body := []byte("pretend this is a png")

	return service.UploadInput{
		UserID:   testUserID,
		FileName: "avatar.png",
		Size:     int64(len(body)),
		File:     bytes.NewReader(body),
	}
}

func TestUploadOrderOfOperations(t *testing.T) {
	t.Parallel()

	d := newDeps()
	svc := newService(t, d)

	avatar, err := svc.Upload(t.Context(), uploadInput())
	require.NoError(t, err)

	// Событие уходит последним: до перевода в uploaded обработчик застал бы
	// запись, по которой миниатюры делать ещё нельзя.
	assert.Equal(t,
		[]string{callValidate, callCreate, callPut, callStatus + "uploaded", callPublish},
		d.log.list())

	assert.Equal(t, testUserID, avatar.UserID)
	assert.Equal(t, domain.UploadStatusUploaded, avatar.UploadStatus)
	assert.Equal(t, domain.ProcessingStatusPending, avatar.ProcessingStatus)
}

func TestUploadStoresOriginal(t *testing.T) {
	t.Parallel()

	d := newDeps()
	svc := newService(t, d)
	in := uploadInput()

	avatar, err := svc.Upload(t.Context(), in)
	require.NoError(t, err)

	assert.Equal(t, domain.OriginalKey(avatar.ID), d.repo.created.S3Key)
	assert.Equal(t, domain.OriginalKey(avatar.ID), d.storage.putKey)
	assert.Equal(t, []byte("pretend this is a png"), d.storage.putBody)

	// Тип содержимого берётся из проверки по сигнатуре файла, а не из имени
	// и не из заголовка запроса.
	assert.Equal(t, "image/png", d.storage.putContentType)
	assert.Equal(t, "image/png", d.repo.created.MimeType)

	// Размеры известны из заголовка изображения и в базу попадают сразу:
	// перечитывать их из хранилища ради метаданных незачем.
	assert.Equal(t, 800, d.repo.created.Width)
	assert.Equal(t, 600, d.repo.created.Height)
	assert.Equal(t, "avatar.png", d.repo.created.FileName)
	assert.Equal(t, in.Size, d.repo.created.SizeBytes)
}

func TestUploadPublishesEvent(t *testing.T) {
	t.Parallel()

	d := newDeps()
	svc := newService(t, d)

	avatar, err := svc.Upload(t.Context(), uploadInput())
	require.NoError(t, err)
	require.Len(t, d.publisher.events, 1)

	event, ok := d.publisher.events[0].(broker.AvatarUploadEvent)
	require.True(t, ok, "событие загрузки")

	assert.Equal(t, avatar.ID.String(), event.AvatarID)
	assert.Equal(t, testUserID, event.UserID)
	assert.Equal(t, domain.OriginalKey(avatar.ID), event.S3Key)
	assert.NotEmpty(t, event.MessageID)
}

func TestUploadValidationFails(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
	}{
		{name: "unsupported format", err: domain.ErrUnsupportedFormat},
		{name: "image too big", err: domain.ErrImageTooBig},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			d := newDeps()
			d.validator.err = tc.err
			svc := newService(t, d)

			_, err := svc.Upload(t.Context(), uploadInput())
			require.ErrorIs(t, err, tc.err)

			// Ни записи, ни файла: непринятое изображение не оставляет следов.
			assert.Equal(t, []string{callValidate}, d.log.list())
		})
	}
}

func TestUploadStorageFails(t *testing.T) {
	t.Parallel()

	d := newDeps()
	d.storage.putErr = errStorage
	svc := newService(t, d)

	_, err := svc.Upload(t.Context(), uploadInput())
	require.ErrorIs(t, err, errStorage)

	assert.Equal(t, []domain.UploadStatus{domain.UploadStatusFailed}, d.repo.statuses)
	assert.Empty(t, d.publisher.events, "событие о ненагруженном оригинале не отправляется")
}

// Неудача перевода в failed не подменяет собой отказ хранилища: наружу уходит
// причина, ради которой сюда пришли.
func TestUploadStorageAndStatusFail(t *testing.T) {
	t.Parallel()

	d := newDeps()
	d.storage.putErr = errStorage
	d.repo.statusErr = errors.New("database is unavailable")
	svc := newService(t, d)

	_, err := svc.Upload(t.Context(), uploadInput())
	require.ErrorIs(t, err, errStorage)
}

func TestUploadCreateFails(t *testing.T) {
	t.Parallel()

	d := newDeps()
	d.repo.createErr = errors.New("database is unavailable")
	svc := newService(t, d)

	_, err := svc.Upload(t.Context(), uploadInput())
	require.ErrorIs(t, err, d.repo.createErr)

	assert.Equal(t, []string{callValidate, callCreate}, d.log.list())
}

func TestUploadStatusFails(t *testing.T) {
	t.Parallel()

	d := newDeps()
	d.repo.statusErr = errors.New("database is unavailable")
	svc := newService(t, d)

	_, err := svc.Upload(t.Context(), uploadInput())
	require.ErrorIs(t, err, d.repo.statusErr)

	assert.Empty(t, d.publisher.events, "событие о записи в неизвестном статусе не отправляется")
	assert.Equal(t,
		[]domain.UploadStatus{domain.UploadStatusUploaded, domain.UploadStatusFailed},
		d.repo.statuses, "после отказа запись переводится в failed")
}

// Оригинал уже в хранилище, запись переведена в uploaded,
// и её переопубликует reconciler; заставлять клиента
// заливать файл заново незачем.
func TestUploadSurvivesPublishFailure(t *testing.T) {
	t.Parallel()

	d := newDeps()
	d.publisher.err = errors.New("broker is unavailable")
	svc := newService(t, d)

	avatar, err := svc.Upload(t.Context(), uploadInput())
	require.NoError(t, err)

	assert.Equal(t, domain.UploadStatusUploaded, avatar.UploadStatus)
	assert.Equal(t, domain.ProcessingStatusPending, avatar.ProcessingStatus)
}

// Клиент, отвалившийся сразу после заливки, не должен оставлять запись
// в uploading с уже лежащим в хранилище
// файлом: такое состояние не подбирает никто.
func TestUploadFinishesAfterClientLeaves(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	d := newDeps()
	d.repo.onCreate = cancel
	svc := newService(t, d)

	avatar, err := svc.Upload(ctx, uploadInput())
	require.NoError(t, err)

	assert.NoError(t, d.storage.putCtxErr, "отменённый контекст запроса не доходит до хранилища")
	assert.Equal(t, domain.UploadStatusUploaded, avatar.UploadStatus)
	assert.Len(t, d.publisher.events, 1)
}
