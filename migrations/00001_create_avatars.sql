-- +goose Up
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
    upload_status VARCHAR(50) NOT NULL DEFAULT 'uploading',
    processing_status VARCHAR(50) NOT NULL DEFAULT 'pending',
    retry_count INT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ,

    -- Перечни статусов заданы в internal/domain; здесь они продублированы,
    -- чтобы недопустимое значение отвергала сама база.
    CONSTRAINT avatars_upload_status_check
        CHECK (upload_status IN ('uploading', 'uploaded', 'failed')),
    CONSTRAINT avatars_processing_status_check
        CHECK (processing_status IN ('pending', 'processing', 'completed', 'failed'))
);

-- Частичный индекс: живые аватары пользователя выбираются постоянно,
-- удалённые не выбираются никогда.
CREATE INDEX idx_avatars_user_id ON avatars(user_id) WHERE deleted_at IS NULL;

-- Ни один запрос API этим индексом не пользуется: он существует ради
-- добора зависших загрузок — записей в состоянии uploaded + pending,
-- по которым событие в брокер так и не ушло.
--
-- updated_at — третьей колонкой: по нему идёт и отсечка «старше пяти минут»,
-- и упорядочивание выборки, а без него база сортирует отобранное отдельным
-- шагом. Удалённые записи в добор не попадают никогда, поэтому индекс частичный.
CREATE INDEX idx_avatars_status ON avatars(upload_status, processing_status, updated_at)
    WHERE deleted_at IS NULL;

-- +goose Down
DROP TABLE avatars;
