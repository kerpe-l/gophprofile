package web

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// Gallery показывает аватары пользователя от новых к старым. Пустая галерея —
// не отказ: у пользователя может не быть ни одного аватара.
func (h *Handlers) Gallery(w http.ResponseWriter, r *http.Request) {
	userID := chi.URLParam(r, paramUserID)

	avatars, err := h.svc.ListByUser(r.Context(), userID)
	if err != nil {
		h.renderFailure(w, r, err)

		return
	}

	h.render(w, r, http.StatusOK, h.pages.gallery, newGalleryView(userID, avatars))
}
