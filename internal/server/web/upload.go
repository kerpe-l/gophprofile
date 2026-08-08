package web

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/kerpe-l/gophprofile/internal/domain"
	"github.com/kerpe-l/gophprofile/internal/server/service"
)

// maxMultipartMemory — сколько разобранной формы держится в памяти;
// остаток уходит во временный файл, который удаляется до выхода из хендлера.
const maxMultipartMemory = 1 << 20

// Пределы длины значений, попадающих в колонки базы: длиннее — отказ формы,
// а не ошибка вставки.
const (
	maxUserIDLen   = 255
	maxFileNameLen = 255
)

// Ошибки формы: причины, по которым загрузка не доходит до сервиса.
var (
	// errMissingUserID — владелец в форме не указан.
	errMissingUserID = errors.New("missing user id")
	// errUserIDTooLong — идентификатор пользователя длиннее своей колонки.
	errUserIDTooLong = errors.New("user id is too long")
	// errMissingFile — файл к форме не приложен.
	errMissingFile = errors.New("missing file field")
	// errFileNameTooLong — имя файла длиннее своей колонки.
	errFileNameTooLong = errors.New("file name is too long")
	// errMalformedBody — тело не разбирается как multipart/form-data.
	errMalformedBody = errors.New("malformed multipart body")
)

// UploadForm показывает форму загрузки. Владелец из query подставляется
// в поле: после просмотра галереи в форму возвращаются с ним же.
func (h *Handlers) UploadForm(w http.ResponseWriter, r *http.Request) {
	userID := r.URL.Query().Get(paramUserID)

	h.render(w, r, http.StatusOK, h.pages.upload, h.newUploadView(userID, ""))
}

// Upload принимает форму и уводит на галерею владельца. Редирект отвечает
// на POST кодом 303: обновление страницы иначе повторяло бы загрузку.
// Отказ возвращает ту же форму с сообщением и сохранённым владельцем.
func (h *Handlers) Upload(w http.ResponseWriter, r *http.Request) {
	userID, err := h.accept(r)
	if err != nil {
		status, message := h.describeError(err)
		h.logFailure(r, status, err)

		h.render(w, r, status, h.pages.upload, h.newUploadView(userID, message))

		return
	}

	http.Redirect(w, r, GalleryURL(userID), http.StatusSeeOther)
}

// accept разбирает форму и передаёт изображение сервису. Владелец
// возвращается и при отказе — форму показывают заполненной.
func (h *Handlers) accept(r *http.Request) (string, error) {
	if err := r.ParseMultipartForm(maxMultipartMemory); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return "", fmt.Errorf("parse upload form: %w", domain.ErrTooLarge)
		}

		return "", fmt.Errorf("parse upload form: %w", errMalformedBody)
	}

	defer h.removeTempFiles(r)

	// Только тело формы: FormValue отдал бы предпочтение одноимённому
	// параметру строки запроса.
	userID := strings.TrimSpace(r.PostFormValue(paramUserID))

	switch {
	case userID == "":
		return "", errMissingUserID
	case len(userID) > maxUserIDLen:
		return userID, errUserIDTooLong
	}

	file, header, err := r.FormFile(fieldFile)
	if err != nil {
		if errors.Is(err, http.ErrMissingFile) {
			return userID, errMissingFile
		}

		return userID, fmt.Errorf("read form field %s: %w", fieldFile, errMalformedBody)
	}

	defer h.closeFile(r.Context(), file)

	switch {
	case header.Size > h.maxUpload:
		return userID, fmt.Errorf("upload of %d bytes: %w", header.Size, domain.ErrTooLarge)
	case len(header.Filename) > maxFileNameLen:
		return userID, errFileNameTooLong
	}

	_, err = h.svc.Upload(r.Context(), service.UploadInput{
		UserID:   userID,
		FileName: header.Filename,
		Size:     header.Size,
		File:     file,
	})
	if err != nil {
		return userID, err
	}

	return userID, nil
}

// closeFile закрывает загруженный файл.
func (h *Handlers) closeFile(ctx context.Context, file io.Closer) {
	if err := file.Close(); err != nil {
		h.log.WarnContext(ctx, "close uploaded file", slog.Any("error", err))
	}
}

// removeTempFiles убирает то, что разбор формы положил на диск.
func (h *Handlers) removeTempFiles(r *http.Request) {
	if r.MultipartForm == nil {
		return
	}

	if err := r.MultipartForm.RemoveAll(); err != nil {
		h.log.WarnContext(r.Context(), "remove multipart temp files", slog.Any("error", err))
	}
}
