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

// AllowedFrom возвращает статусы, из которых разрешён переход в s.
// Пустой результат означает, что переходов в s нет вовсе.
func (s UploadStatus) AllowedFrom() []UploadStatus {
	switch s {
	case UploadStatusUploaded, UploadStatusFailed:
		return []UploadStatus{UploadStatusUploading}
	default:
		// uploading ставится вставкой записи; вернуться в него нельзя —
		// повторная загрузка порождает новый аватар, а не переоткрывает старый.
		return nil
	}
}

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

// AllowedFrom возвращает статусы, из которых разрешён переход в s.
// Пустой результат означает, что переходов в s нет вовсе.
//
// completed и failed терминальны: обработка законченного аватара не
// начинается заново — за повтор отвечает лестница retry-очередей, а не
// возврат записи в предыдущий статус.
func (s ProcessingStatus) AllowedFrom() []ProcessingStatus {
	switch s {
	case ProcessingStatusProcessing:
		// processing → processing разрешён: повторная доставка события после
		// падения обработчика застаёт запись уже начатой, и начать заново
		// она обязана.
		return []ProcessingStatus{ProcessingStatusPending, ProcessingStatusProcessing}
	case ProcessingStatusCompleted:
		return []ProcessingStatus{ProcessingStatusProcessing}
	case ProcessingStatusFailed:
		// Неисправимая ошибка распознаётся и до начала обработки: у события
		// с битым или отсутствующим оригиналом до миниатюр не доходит.
		return []ProcessingStatus{ProcessingStatusPending, ProcessingStatusProcessing}
	default:
		// pending ставится вставкой записи.
		return nil
	}
}
