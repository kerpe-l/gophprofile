package http

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// userAvatarContent отдаёт актуальный аватар пользователя, а при его
// отсутствии — заглушку с коротким кешем: раздать изображение всегда
// и есть назначение сервиса.
func (a *api) userAvatarContent(w http.ResponseWriter, r *http.Request) {
	size, err := thumbnailSize(r)
	if err != nil {
		a.respond(w, r, err)

		return
	}

	content, err := a.svc.UserAvatarContent(r.Context(), chi.URLParam(r, paramUserID), size)
	if err != nil {
		a.respond(w, r, err)

		return
	}

	a.writeContent(w, r, content)
}

// userAvatars отдаёт список живых аватаров пользователя от новых к старым.
// Пустой список — не ошибка: у пользователя может не быть ни одного аватара.
func (a *api) userAvatars(w http.ResponseWriter, r *http.Request) {
	avatars, err := a.svc.ListByUser(r.Context(), chi.URLParam(r, paramUserID))
	if err != nil {
		a.respond(w, r, err)

		return
	}

	items := make([]metadataResponse, 0, len(avatars))
	for _, avatar := range avatars {
		items = append(items, newMetadataResponse(avatar))
	}

	writeJSON(r.Context(), w, a.log, http.StatusOK, listResponse{Avatars: items})
}

// deleteUserAvatar удаляет актуальный аватар пользователя — последний
// созданный среди живых. Остальные его аватары не затрагиваются.
func (a *api) deleteUserAvatar(w http.ResponseWriter, r *http.Request) {
	requester, err := requesterID(r)
	if err != nil {
		a.respond(w, r, err)

		return
	}

	if err := a.svc.DeleteCurrent(r.Context(), chi.URLParam(r, paramUserID), requester); err != nil {
		a.respond(w, r, err)

		return
	}

	w.WriteHeader(http.StatusNoContent)
}
