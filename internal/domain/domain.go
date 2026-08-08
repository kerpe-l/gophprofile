// Package domain содержит модель аватара, её статусы и доменные ошибки.
//
// Пакет не импортирует инфраструктуру — ни драйвер БД, ни клиент хранилища,
// ни клиент брокера.
package domain

// UploadStatus — состояние загрузки оригинала в хранилище.
type UploadStatus string

// Значения UploadStatus: запись создана, оригинал доехал, загрузка сорвалась.
const (
	UploadStatusUploading UploadStatus = "uploading"
	UploadStatusUploaded  UploadStatus = "uploaded"
	UploadStatusFailed    UploadStatus = "failed"
)

// AllowedFrom возвращает статусы, из которых разрешён переход в s.
// Пустой результат означает, что переходов в s нет вовсе.
func (s UploadStatus) AllowedFrom() []UploadStatus {
	switch s {
	case UploadStatusUploaded, UploadStatusFailed:
		return []UploadStatus{UploadStatusUploading}
	default:
		// uploading ставится вставкой записи; повторная загрузка порождает
		// новый аватар, а не переоткрывает старый.
		return nil
	}
}

// ProcessingStatus — состояние асинхронной обработки: создания миниатюр.
type ProcessingStatus string

// Значения ProcessingStatus: обработка не начиналась, идёт, завершилась
// успехом или провалилась окончательно.
const (
	ProcessingStatusPending    ProcessingStatus = "pending"
	ProcessingStatusProcessing ProcessingStatus = "processing"
	ProcessingStatusCompleted  ProcessingStatus = "completed"
	ProcessingStatusFailed     ProcessingStatus = "failed"
)

// AllowedFrom возвращает статусы, из которых разрешён переход в s.
// Пустой результат означает, что переходов в s нет вовсе; completed и failed
// терминальны.
func (s ProcessingStatus) AllowedFrom() []ProcessingStatus {
	switch s {
	case ProcessingStatusProcessing:
		// Повторный вход в processing нужен для доставки после сбоя обработчика.
		return []ProcessingStatus{ProcessingStatusPending, ProcessingStatusProcessing}
	case ProcessingStatusCompleted:
		return []ProcessingStatus{ProcessingStatusProcessing}
	case ProcessingStatusFailed:
		// До миниатюр дело могло и не дойти: битый оригинал виден сразу.
		return []ProcessingStatus{ProcessingStatusPending, ProcessingStatusProcessing}
	default:
		// pending ставится вставкой записи.
		return nil
	}
}
