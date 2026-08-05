package s3

import (
	"context"
	"errors"
	"fmt"
	"io"
	"iter"

	"github.com/minio/minio-go/v7"
)

// defaultContentType — тип содержимого для объекта, у которого он не задан.
const defaultContentType = "application/octet-stream"

// Object — открытый на чтение объект хранилища вместе с метаданными,
// которые нужны для заголовков ответа.
//
// Объект обязан быть закрыт: до Close заняты соединение с хранилищем
// и горутина чтения внутри клиента.
type Object struct {
	// ETag — хеш содержимого, каким его отдало хранилище, без обрамляющих
	// кавычек: их добавляет транспорт при записи заголовка.
	ETag string
	// ContentType — тип содержимого, записанный при загрузке.
	ContentType string
	// Size — размер объекта в байтах.
	Size int64

	body *minio.Object
}

// Read читает содержимое объекта. Ошибка не оборачивается: io.EOF обязан
// дойти до io.Copy в исходном виде.
func (o *Object) Read(p []byte) (int, error) {
	return o.body.Read(p)
}

// Close освобождает соединение с хранилищем.
func (o *Object) Close() error {
	if err := o.body.Close(); err != nil {
		return fmt.Errorf("close object: %w", err)
	}

	return nil
}

// Put кладёт объект по ключу, перезаписывая существующий. Размер обязателен:
// без него клиент вынужден буферизовать поток, чтобы собрать multipart.
//
// Повторной попытки при временной ошибке не будет, если r не умеет Seek:
// перемотать уже прочитанный поток нечем.
func (s *Storage) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	ctx, cancel := s.withDeadline(ctx)
	defer cancel()

	if contentType == "" {
		contentType = defaultContentType
	}

	_, err := s.client.PutObject(ctx, s.bucket, key, r, size, minio.PutObjectOptions{
		ContentType: contentType,
	})
	if err != nil {
		return mapError("put object", key, err)
	}

	return nil
}

// Get открывает объект на чтение. Возвращённый объект обязан быть закрыт
// вызывающим, в том числе если чтение прервалось на середине.
//
// Отсутствие объекта — domain.ErrNotFound.
//
// Своего предела времени Get не ставит: тело читается уже после возврата
// из метода, и дедлайн, снятый по выходу, оборвал бы выдачу на середине.
// Ограничить всю выдачу — задача вызывающего: у него есть дедлайн запроса
// или бюджет обработки сообщения.
func (s *Storage) Get(ctx context.Context, key string) (*Object, error) {
	obj, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, mapError("get object", key, err)
	}

	// Запрос к хранилищу клиент откладывает до первого чтения, поэтому Stat
	// зовётся сразу: иначе отсутствие объекта всплыло бы посреди io.Copy,
	// когда статус ответа клиенту уже отправлен.
	info, err := obj.Stat()
	if err != nil {
		// Причина отказа уже есть; ошибка закрытия неоткрывшегося объекта
		// её не уточняет.
		_ = obj.Close()

		return nil, mapError("get object", key, err)
	}

	return &Object{
		ETag:        info.ETag,
		ContentType: info.ContentType,
		Size:        info.Size,
		body:        obj,
	}, nil
}

// Delete удаляет объект по ключу. Удаление идемпотентно: отсутствие объекта
// ошибкой не считается — повторная доставка события удаления штатна.
func (s *Storage) Delete(ctx context.Context, key string) error {
	ctx, cancel := s.withDeadline(ctx)
	defer cancel()

	err := s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{})
	if err != nil && !isNotFound(err) {
		return mapError("delete object", key, err)
	}

	return nil
}

// DeleteMany удаляет объекты одним пакетным запросом. Как и Delete,
// идемпотентно.
//
// Отказ на части ключей не выглядит успехом: ошибки собираются по всем
// объектам и возвращаются вместе.
func (s *Storage) DeleteMany(ctx context.Context, keys []string) error {
	if len(keys) == 0 {
		return nil
	}

	ctx, cancel := s.withDeadline(ctx)
	defer cancel()

	results, err := s.client.RemoveObjectsWithIter(ctx, s.bucket, objectInfos(keys), minio.RemoveObjectsOptions{})
	if err != nil {
		return fmt.Errorf("delete objects: %w", err)
	}

	var errs []error

	for res := range results {
		if res.Err != nil && !isNotFound(res.Err) {
			errs = append(errs, mapError("delete object", res.ObjectName, res.Err))
		}
	}

	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("delete objects: %w", err)
	}

	return nil
}

// objectInfos переводит ключи в вид, которого ждёт пакетное удаление.
func objectInfos(keys []string) iter.Seq[minio.ObjectInfo] {
	return func(yield func(minio.ObjectInfo) bool) {
		for _, key := range keys {
			if !yield(minio.ObjectInfo{Key: key}) {
				return
			}
		}
	}
}
