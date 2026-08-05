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
    upload_status VARCHAR(50) DEFAULT 'uploading',      -- uploading | uploaded | failed
    processing_status VARCHAR(50) DEFAULT 'pending',    -- pending | processing | completed | failed
    retry_count INT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW(),
    deleted_at TIMESTAMPTZ
);

-- Частичный индекс: живые аватары пользователя выбираются постоянно,
-- удалённые не выбираются никогда.
CREATE INDEX idx_avatars_user_id ON avatars(user_id) WHERE deleted_at IS NULL;

-- Ни один запрос API этим индексом не пользуется: он существует ради
-- добора зависших загрузок — записей в состоянии uploaded + pending,
-- по которым событие в брокер так и не ушло.
CREATE INDEX idx_avatars_status ON avatars(upload_status, processing_status);

-- +goose Down
DROP TABLE avatars;
