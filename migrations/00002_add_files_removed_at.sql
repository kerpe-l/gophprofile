-- +goose Up
ALTER TABLE avatars ADD COLUMN files_removed_at TIMESTAMPTZ;

-- Записи, чьи файлы ещё не убраны из хранилища: удалённые и сорвавшиеся
-- загрузки. Статус записан литералом и в запросе добора: generic-план
-- подготовленного запроса с параметром частичный индекс не выберет.
CREATE INDEX idx_avatars_uncleaned ON avatars(updated_at)
    WHERE files_removed_at IS NULL AND (deleted_at IS NOT NULL OR upload_status = 'failed');

-- +goose Down
DROP INDEX idx_avatars_uncleaned;
ALTER TABLE avatars DROP COLUMN files_removed_at;
