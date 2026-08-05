# GophProfile — спецификация


## 1. Назначение

Микросервис хранения и раздачи аватарок. Пользователь загружает изображение; сервис сохраняет оригинал, асинхронно создаёт миниатюры и отдаёт аватар по запросу. Если аватара нет — отдаётся стандартная заглушка.

## 2. Технологические решения

| Решение | Выбор | Обоснование |
|---|---|---|
| Язык | Go 1.25+ | — |
| HTTP-роутер | Chi | хендлеры остаются `http.HandlerFunc`, тестируются через `httptest` |
| БД метаданных | PostgreSQL 16 | — |
| Драйвер БД | `jackc/pgx/v5` | — |
| Файловое хранилище | MinIO (S3 API), клиент `minio-go/v7` | локально в Docker, совместимо с AWS S3 |
| Брокер | RabbitMQ, exchange `avatars.exchange` типа `topic`, клиент `rabbitmq/amqp091-go` | — |
| Обработка изображений | `disintegration/imaging` | даёт `imaging.Fill` — центрированный кроп в квадрат |
| Декодирование WebP | `golang.org/x/image/webp` | — |
| Миграции | `pressly/goose/v3` + `embed.FS` | — |
| Деплой | Docker Compose (спринт 1), Kubernetes (позже) | |

## 3. Компоненты

- **server** — REST API + веб-интерфейс. Принимает загрузку, пишет метаданные в PG, кладёт оригинал в S3, публикует событие.
- **worker** — подписан на события. Создаёт миниатюры (100x100, 300x300), удаляет файлы из S3 при удалении аватара, добирает зависшие загрузки (§6.3).
- **migrator** — одноразовый контейнер, применяет миграции до старта server и worker. DDL не выполняют ни server, ни worker — иначе они гоняются друг с другом за блокировки при параллельном старте.
- **PostgreSQL** — метаданные аватарок.
- **MinIO** — оригиналы и миниатюры.
- **RabbitMQ** — очередь событий обработки/удаления.

## 4. REST API

Базовый префикс: `/api/v1`.

### 4.0 Модель безопасности

`X-User-ID` — **не аутентификация**. Заголовок проставляется доверенным API-gateway перед сервисом; GophProfile принимает его на веру и лишь сверяет владение при удалении. Сервис не выпускает и не проверяет токены. Выставлять его напрямую в интернет нельзя.

Все `GET`-эндпоинты публичны и `X-User-ID` не требуют — аватарки читаются кем угодно. Заголовок обязателен только для `POST` и `DELETE`.

### 4.1 Загрузка

```
POST /api/v1/avatars
Headers:  X-User-ID: string (required)
Body:     multipart/form-data, поле file (required, max 10MB)

201 → { "id": uuid, "user_id": string, "url": string,
        "status": "processing", "created_at": RFC3339 }
400 → { "error": "Invalid file format", "details": "Supported formats: jpeg, png, webp" }
413 → { "error": "File too large", "max_size": 10485760 }
```

Валидация, по порядку:

1. `http.MaxBytesReader` на теле **до** парсинга multipart — 413 отдаётся без вычитывания всех 10MB+.
2. MIME по magic bytes через `http.DetectContentType` (первые 512 байт): `image/jpeg`, `image/png`, `image/webp`. Заголовок `Content-Type` от клиента и расширение имени файла игнорируются.
3. `image.DecodeConfig` — до полного декодирования: ширина·высота ≤ 50 Мпикс. Защита от decompression bomb: 10MB PNG разворачивается в 30000×30000 и кладёт процесс по памяти раньше, чем сработает любой лимит на размер файла.

Анимированный WebP не поддерживается (декодер `x/image/webp` его не читает) — 400.

### 4.2 Получение

```
GET /api/v1/avatars/{avatar_id}
GET /api/v1/users/{user_id}/avatar

Query (опционально):
  size: "100x100" | "300x300" | "original"   (default: original)

200 → бинарные данные изображения
      Content-Type: image/*
      Cache-Control: max-age=86400
      ETag: "<hash>"
304 → если совпал If-None-Match
404 → { "error": "Avatar not found" }
```

Раздача — стримингом через `io.Copy` из S3, без буферизации тела в память.

`ETag` берётся из S3 (MinIO возвращает md5 объекта); поддерживается `If-None-Match` → 304.

**Миниатюра ещё не готова.** Если запрошен `size=100x100|300x300`, а `processing_status` ещё не `completed`, отдаётся оригинал с `Cache-Control: max-age=60`. Для аватарки временно неточный размер лучше, чем 404, а короткий TTL гарантирует, что клиент скоро перезапросит уже готовую миниатюру.

**Заглушка.** `GET /users/{user_id}/avatar` отдаёт актуальный аватар пользователя (§4.4); если аватара нет — **200 с заглушкой**, это прямое назначение сервиса из §1. Дополнительно:

- `X-Avatar-Default: true` — чтобы клиент мог отличить заглушку от настоящего аватара;
- `Cache-Control: max-age=300` вместо суток — иначе заглушка залипнет в кешах и первая загрузка аватара не подхватится;
- ETag заглушки стабилен между рестартами.

Заглушка лежит в `embed.FS` внутри бинарника, не в S3 — тогда fallback работает даже при недоступном MinIO.

`GET /avatars/{avatar_id}` заглушку **не** отдаёт: запрошен конкретный объект, которого нет — строго 404.

Конвертация формата на лету (`?format=`) в спринт 1 не входит: энкодер WebP требует cgo-биндинга к libwebp, а конвертация только между jpeg и png самостоятельной ценности не имеет. Отдаётся то, что лежит в хранилище.

### 4.3 Метаданные и список

```
GET /api/v1/avatars/{avatar_id}/metadata
200 → { id, user_id, file_name, mime_type, size,
        dimensions: {width, height},
        thumbnails: [{size, url}],
        created_at, updated_at }

GET /api/v1/users/{user_id}/avatars
200 → { "avatars": [ ...metadata... ] }
```

`url` в `thumbnails[]` — относительный путь вида `/api/v1/avatars/{id}?size=100x100`. Абсолютные URL не строим: сервис не знает своего внешнего адреса за прокси, а угадывание по `Host` ломается при смене домена.

`GET /users/{user_id}/avatars` возвращает только живые (`deleted_at IS NULL`) аватары, отсортированные по `created_at DESC`.

### 4.4 Удаление

```
DELETE /api/v1/avatars/{avatar_id}
DELETE /api/v1/users/{user_id}/avatar
Headers: X-User-ID: string (required)

204 → No Content
403 → { "error": "Forbidden", "details": "You can only delete your own avatars" }
404 → { "error": "Avatar not found" }
```

**Актуальный аватар** пользователя — последний по `created_at` среди записей с `deleted_at IS NULL`. Модель допускает несколько аватаров у пользователя (§4.3 отдаёт список), поэтому определение нужно явно.

`DELETE /users/{user_id}/avatar` удаляет **только актуальный** аватар — симметрично `GET /users/{user_id}/avatar`. Массового удаления всех аватаров пользователя в API нет.

Удаление идемпотентно: повторный `DELETE` уже удалённого аватара → 404.

Мягкое удаление в БД (`deleted_at`), файлы из S3 удаляет worker асинхронно.

### 4.5 Служебные

```
GET /health
200/503 → { "status": "ok"|"degraded",
            "components": { "db": ..., "s3": ..., "broker": ... } }
```

`degraded` + 503, если недоступен хотя бы один компонент. Каждая проверка — со своим таймаутом (2s), иначе `/health` подвисает вместе с проверяемой зависимостью и перестаёт быть индикатором.

### 4.6 Веб-интерфейс

```
GET  /web/upload            — форма загрузки (превью, drag&drop — опционально)
POST /web/upload            — обработка загрузки
GET  /web/gallery/{user_id} — галерея аватарок
```

Свой минимальный фронтенд на `html/template`, шаблоны и статика через `embed.FS`, без JS-сборки.

## 5. Модель данных

```sql
CREATE TABLE avatars (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id VARCHAR(255) NOT NULL,
    file_name VARCHAR(255) NOT NULL,
    mime_type VARCHAR(100) NOT NULL,
    size_bytes BIGINT NOT NULL,
    width INT,
    height INT,
    s3_key VARCHAR(500) NOT NULL,
    thumbnail_s3_keys JSONB,
    upload_status VARCHAR(50) DEFAULT 'uploading',      -- uploading | uploaded | failed
    processing_status VARCHAR(50) DEFAULT 'pending',    -- pending | processing | completed | failed
    retry_count INT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW(),
    deleted_at TIMESTAMPTZ
);

CREATE INDEX idx_avatars_user_id ON avatars(user_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_avatars_status  ON avatars(upload_status, processing_status);
```

`width`/`height` добавлены к исходной схеме — §4.3 требует `dimensions` в метаданных, а читать их из S3 на каждый запрос метаданных незачем: они известны на этапе загрузки из `image.DecodeConfig`.

`idx_avatars_status` не используется ни одним запросом API — он существует ради reconciler'а (§6.3).

`thumbnail_s3_keys` — объект `{"100x100": "thumbnails/<id>/100x100", "300x300": "..."}`.

S3-ключи: `originals/{avatar_id}` и `thumbnails/{avatar_id}/{size}` — генерируются сервисом, имя файла пользователя в ключах не участвует.

### 5.1 Формат миниатюр

Миниатюры всегда сохраняются в **JPEG, quality 85**, независимо от формата оригинала. Единственный энкодер, всё из stdlib, никакого cgo. Плата — прозрачность PNG схлопывается в белый фон; для аватарок это приемлемо.

Оригинал хранится байт в байт как загружен, в исходном формате.

## 6. События брокера

Exchange `avatars.exchange` (topic). Routing keys: `avatar.uploaded`, `avatar.deleted`.

```go
type AvatarUploadEvent struct {
    MessageID string `json:"message_id"` // uuid, для идемпотентности
    AvatarID  string `json:"avatar_id"`
    UserID    string `json:"user_id"`
    S3Key     string `json:"s3_key"`
}

type AvatarDeleteEvent struct {
    MessageID string   `json:"message_id"`
    AvatarID  string   `json:"avatar_id"`
    S3Keys    []string `json:"s3_keys"`
}
```

### 6.1 Идемпотентность

Worker перед обработкой проверяет `processing_status` в БД; событие для уже обработанного или удалённого аватара — ack без работы. Повторная доставка не должна порождать повторную запись в S3.

### 6.2 Retry и DLQ

У RabbitMQ нет встроенного счётчика попыток с задержкой, поэтому экспоненциальный backoff строится лестницей retry-очередей: каждая имеет свой `x-message-ttl` и по истечении dead-letter'ит сообщение обратно в основную очередь.

```
avatars.exchange (topic)
  └─→ avatars.process         [x-dead-letter-exchange: avatars.retry]
avatars.retry (direct)
  ├─→ avatars.retry.5s        [ttl=5s,  dlx → avatars.exchange]
  ├─→ avatars.retry.30s       [ttl=30s, dlx → avatars.exchange]
  └─→ avatars.retry.5m        [ttl=5m,  dlx → avatars.exchange]
avatars.dlq (direct)
  └─→ avatars.dead
```

- Число попыток считается по заголовку `x-death` (RabbitMQ ведёт его сам) — уровень задержки выбирается по счётчику: 1→5s, 2→30s, 3+→5m.
- После **5** попыток сообщение уходит в `avatars.dlq`, в БД проставляется `processing_status = 'failed'`.
- Ошибки делятся на retryable (сеть, временная недоступность S3/PG) и non-retryable (битый файл, неподдерживаемый формат, аватар удалён). Non-retryable → сразу `failed` + ack, без прогона по лестнице: перекодировать битый JPEG через 5 минут не выйдет.
- `sleep` в консьюмере вместо очередей не годится: блокирует префетч и держит соединение.

### 6.3 Reconciler

`publish` в брокер не входит в транзакцию с записью в PG и загрузкой в S3 — атомарности между тремя системами нет. Порядок операций при загрузке:

```
INSERT (upload_status='uploading') → PUT в S3 → UPDATE 'uploaded' → publish
```

Если процесс умер после `PUT`, но до `publish`, аватар навсегда остаётся в `uploaded` + `pending`. Поэтому worker раз в минуту выбирает записи в состоянии `uploaded` + `pending` старше 5 минут (ровно то, подо что заведён `idx_avatars_status`) и переопубликовывает для них событие. Идемпотентность (§6.1) делает повторную публикацию безопасной.

Полноценный transactional outbox корректнее, но требует отдельной таблицы и публишера — оставлено как возможное развитие.

## 7. Нефункциональные требования

- Покрытие unit-тестами > 50%.
- `golangci-lint` без ошибок.
- Docker Compose поднимает всё окружение одной командой.
- Секреты — только через env.

## 8. Бонус (безопасность, если успеем)

- Rate limiting для API.
- CORS-настройки.
- Валидация формата User-ID.
- Дедупликация по sha256 содержимого.
- Конвертация формата на лету (`?format=`) — требует cgo, см. §4.2.
