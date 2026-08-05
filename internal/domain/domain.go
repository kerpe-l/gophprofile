// Package domain содержит модель аватара, её статусы и доменные ошибки.
//
// Пакет не импортирует инфраструктуру — ни драйвер БД, ни клиент хранилища,
// ни клиент брокера: иначе доменные ошибки потянули бы за собой инфраструктуру
// в каждый тест, который их сравнивает.
package domain

// UploadStatus — состояние загрузки оригинала в хранилище.
type UploadStatus string

// Значения UploadStatus.
const (
	// UploadStatusUploading — запись создана, оригинал ещё не в хранилище.
	UploadStatusUploading UploadStatus = "uploading"
	// UploadStatusUploaded — оригинал лежит в хранилище.
	UploadStatusUploaded UploadStatus = "uploaded"
	// UploadStatusFailed — загрузка оригинала не удалась.
	UploadStatusFailed UploadStatus = "failed"
)

// ProcessingStatus — состояние асинхронной обработки: создания миниатюр.
type ProcessingStatus string

// Значения ProcessingStatus.
const (
	// ProcessingStatusPending — обработка ещё не начиналась.
	ProcessingStatusPending ProcessingStatus = "pending"
	// ProcessingStatusProcessing — миниатюры создаются прямо сейчас.
	ProcessingStatusProcessing ProcessingStatus = "processing"
	// ProcessingStatusCompleted — все миниатюры готовы.
	ProcessingStatusCompleted ProcessingStatus = "completed"
	// ProcessingStatusFailed — обработка провалилась окончательно, повторов не будет.
	ProcessingStatusFailed ProcessingStatus = "failed"
)
