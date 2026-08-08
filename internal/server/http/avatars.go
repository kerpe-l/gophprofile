package http

import (
	"errors"
	"fmt"
	"log/slog"
	"mime/multipart"
	"net/http"

	"github.com/kerpe-l/gophprofile/internal/domain"
	"github.com/kerpe-l/gophprofile/internal/server/service"
)

// Имена полей формы загрузки. Файл принимается под двумя именами;
// приоритет у fieldFile, если присутствуют оба.
const (
	fieldFile  = "file"
	fieldImage = "image"
)

// maxMultipartMemory — сколько разобранной формы держится в памяти;
// остаток уходит во временный файл, который удаляется до выхода из хендлера.
const maxMultipartMemory = 1 << 20

// upload принимает изображение и ставит аватар в очередь на обработку.
func (a *api) upload(w http.ResponseWriter, r *http.Request) {
	userID, err := requesterID(r)
	if err != nil {
		a.respond(w, r, err)

		return
	}

	file, header, err := a.formFile(r)
	if err != nil {
		a.respond(w, r, err)

		return
	}

	defer a.closeForm(r, file)

	if header.Size > a.maxUpload {
		a.respond(w, r, fmt.Errorf("upload of %d bytes: %w", header.Size, domain.ErrTooLarge))

		return
	}

	if len(header.Filename) > maxFileNameLen {
		a.respond(w, r, errFileNameTooLong)

		return
	}

	avatar, err := a.svc.Upload(r.Context(), service.UploadInput{
		UserID:   userID,
		FileName: header.Filename,
		Size:     header.Size,
		File:     file,
	})
	if err != nil {
		a.respond(w, r, err)

		return
	}

	writeJSON(r.Context(), w, a.log, http.StatusCreated, newUploadResponse(avatar))
}

// formFile достаёт загружаемый файл из формы.
//
// Превышение лимита размера распознаётся здесь: MaxBytesReader обрывает
// чтение на разборе формы, и без этой ветки отказ по размеру выглядел бы
// как испорченное тело.
func (a *api) formFile(r *http.Request) (multipart.File, *multipart.FileHeader, error) {
	if err := r.ParseMultipartForm(maxMultipartMemory); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return nil, nil, fmt.Errorf("parse upload form: %w", domain.ErrTooLarge)
		}

		return nil, nil, fmt.Errorf("parse upload form: %w", errMalformedBody)
	}

	for _, field := range []string{fieldFile, fieldImage} {
		file, header, err := r.FormFile(field)

		switch {
		case err == nil:
			return file, header, nil
		case errors.Is(err, http.ErrMissingFile):
			continue
		default:
			return nil, nil, fmt.Errorf("read form field %s: %w", field, errMalformedBody)
		}
	}

	return nil, nil, errMissingFile
}

// closeForm закрывает загруженный файл и убирает временные файлы формы:
// без этого содержимое больших загрузок остаётся на диске до перезапуска.
func (a *api) closeForm(r *http.Request, file multipart.File) {
	if err := file.Close(); err != nil {
		a.log.WarnContext(r.Context(), "close uploaded file", slog.Any("error", err))
	}

	if r.MultipartForm == nil {
		return
	}

	if err := r.MultipartForm.RemoveAll(); err != nil {
		a.log.WarnContext(r.Context(), "remove multipart temp files", slog.Any("error", err))
	}
}

// avatarContent отдаёт изображение конкретного аватара.
//
// Заглушка здесь не подставляется ни при каком исходе: запрошен объект
// по идентификатору, и его отсутствие — 404.
func (a *api) avatarContent(w http.ResponseWriter, r *http.Request) {
	id, err := avatarID(r)
	if err != nil {
		a.respond(w, r, err)

		return
	}

	size, err := thumbnailSize(r)
	if err != nil {
		a.respond(w, r, err)

		return
	}

	content, err := a.svc.AvatarContent(r.Context(), id, size)
	if err != nil {
		a.respond(w, r, err)

		return
	}

	a.writeContent(w, r, content)
}

func (a *api) avatarMetadata(w http.ResponseWriter, r *http.Request) {
	id, err := avatarID(r)
	if err != nil {
		a.respond(w, r, err)

		return
	}

	avatar, err := a.svc.Metadata(r.Context(), id)
	if err != nil {
		a.respond(w, r, err)

		return
	}

	writeJSON(r.Context(), w, a.log, http.StatusOK, newMetadataResponse(avatar))
}

func (a *api) deleteAvatar(w http.ResponseWriter, r *http.Request) {
	userID, err := requesterID(r)
	if err != nil {
		a.respond(w, r, err)

		return
	}

	id, err := avatarID(r)
	if err != nil {
		a.respond(w, r, err)

		return
	}

	if err := a.svc.Delete(r.Context(), id, userID); err != nil {
		a.respond(w, r, err)

		return
	}

	w.WriteHeader(http.StatusNoContent)
}
