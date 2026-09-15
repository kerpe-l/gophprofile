package s3

import (
	"context"
	"errors"
	"fmt"
	"io"
	"iter"

	"github.com/minio/minio-go/v7"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/kerpe-l/gophprofile/internal/domain"
	"github.com/kerpe-l/gophprofile/internal/observability"
)

// defaultContentType — тип содержимого для объекта, у которого он не задан.
const defaultContentType = "application/octet-stream"

// Put кладёт объект по ключу, перезаписывая существующий. Размер обязателен:
// без него клиент вынужден буферизовать поток, чтобы собрать multipart.
//
// Повторной попытки при временной ошибке не будет, если r не умеет Seek:
// перемотать уже прочитанный поток нечем.
func (s *Storage) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	ctx, span := s.tracer.Start(ctx, "s3.put", trace.WithAttributes(
		attribute.String(attrBucket, s.bucket),
		attribute.String(attrKey, key),
	))
	defer span.End()

	ctx, cancel := s.withDeadline(ctx)
	defer cancel()

	if contentType == "" {
		contentType = defaultContentType
	}

	_, err := s.client.PutObject(ctx, s.bucket, key, r, size, minio.PutObjectOptions{
		ContentType: contentType,
	})
	if err != nil {
		return observability.SpanError(span, mapError("put object", key, err))
	}

	return nil
}

// Get открывает объект на чтение; отсутствие объекта — domain.ErrNotFound.
// Тело обязано быть закрыто вызывающим, в том числе при обрыве чтения.
//
// Своего предела времени Get не ставит: тело читается после возврата
// из метода. Ограничить выдачу — задача вызывающего. Спан по той же причине
// покрывает только открытие объекта, без чтения тела.
func (s *Storage) Get(ctx context.Context, key string) (domain.StoredObject, error) {
	ctx, span := s.tracer.Start(ctx, "s3.get", trace.WithAttributes(
		attribute.String(attrBucket, s.bucket),
		attribute.String(attrKey, key),
	))
	defer span.End()

	obj, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return domain.StoredObject{}, observability.SpanError(span, mapError("get object", key, err))
	}

	// Запрос к хранилищу клиент откладывает до первого чтения, поэтому Stat
	// зовётся сразу: иначе отсутствие объекта всплыло бы посреди io.Copy,
	// когда статус ответа клиенту уже отправлен.
	info, err := obj.Stat()
	if err != nil {
		// Причина отказа уже есть; ошибка закрытия неоткрывшегося объекта
		// её не уточняет.
		_ = obj.Close()

		return domain.StoredObject{}, observability.SpanError(span, mapError("get object", key, err))
	}

	return domain.StoredObject{
		Body:        obj,
		ETag:        info.ETag,
		ContentType: info.ContentType,
		Size:        info.Size,
	}, nil
}

// DeleteMany удаляет объекты одним пакетным запросом. Удаление идемпотентно:
// отсутствие объекта ошибкой не считается — повторная доставка события
// удаления штатна.
//
// Отказ на части ключей не выглядит успехом: ошибки собираются по всем
// объектам и возвращаются вместе.
func (s *Storage) DeleteMany(ctx context.Context, keys []string) error {
	if len(keys) == 0 {
		return nil
	}

	ctx, span := s.tracer.Start(ctx, "s3.delete_many", trace.WithAttributes(
		attribute.String(attrBucket, s.bucket),
		attribute.Int("s3.keys", len(keys)),
	))
	defer span.End()

	ctx, cancel := s.withDeadline(ctx)
	defer cancel()

	results, err := s.client.RemoveObjectsWithIter(ctx, s.bucket, objectInfos(keys), minio.RemoveObjectsOptions{})
	if err != nil {
		return observability.SpanError(span, fmt.Errorf("delete objects: %w", err))
	}

	var errs []error

	for res := range results {
		if res.Err != nil && !isNotFound(res.Err) {
			errs = append(errs, mapError("delete object", res.ObjectName, res.Err))
		}
	}

	if err := errors.Join(errs...); err != nil {
		return observability.SpanError(span, fmt.Errorf("delete objects: %w", err))
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
