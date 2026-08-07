package http_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kerpe-l/gophprofile/internal/domain"
)

func TestUserAvatars(t *testing.T) {
	t.Parallel()

	first := completedAvatar()

	second := completedAvatar()
	second.ProcessingStatus = domain.ProcessingStatusPending

	svc := &fakeService{avatars: []domain.Avatar{first, second}}
	router := newRouter(t, svc, nil)

	w := do(t, router, request(t, http.MethodGet, "/api/v1/users/"+testUserID+"/avatars"))

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, testUserID, svc.gotUserID)

	var body struct {
		Avatars []struct {
			ID         string `json:"id"`
			Thumbnails []struct {
				Size string `json:"size"`
			} `json:"thumbnails"`
		} `json:"avatars"`
	}

	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Len(t, body.Avatars, 2)

	assert.Equal(t, first.ID.String(), body.Avatars[0].ID)
	assert.Len(t, body.Avatars[0].Thumbnails, 1)
	assert.Empty(t, body.Avatars[1].Thumbnails)
}

// TestUserAvatarsEmpty — отсутствие аватаров не ошибка, а пустой список;
// в JSON он остаётся массивом, а не null.
func TestUserAvatarsEmpty(t *testing.T) {
	t.Parallel()

	svc := &fakeService{}
	router := newRouter(t, svc, nil)

	w := do(t, router, request(t, http.MethodGet, "/api/v1/users/nobody/avatars"))

	require.Equal(t, http.StatusOK, w.Code)
	assert.JSONEq(t, `{"avatars":[]}`, w.Body.String())
}

func TestDeleteUserAvatar(t *testing.T) {
	t.Parallel()

	svc := &fakeService{}
	router := newRouter(t, svc, nil)

	r := request(t, http.MethodDelete, "/api/v1/users/"+testUserID+"/avatar")
	r.Header.Set("X-User-ID", testUserID)

	w := do(t, router, r)

	require.Equal(t, http.StatusNoContent, w.Code)
	assert.Equal(t, testUserID, svc.gotUserID)
	assert.Equal(t, testUserID, svc.gotRequeste)
}

// TestDeleteUserAvatarForeign — сверку владельца делает сервис, транспорт
// лишь переводит её отказ в 403.
func TestDeleteUserAvatarForeign(t *testing.T) {
	t.Parallel()

	svc := &fakeService{err: domain.ErrForbidden}
	router := newRouter(t, svc, nil)

	r := request(t, http.MethodDelete, "/api/v1/users/"+testUserID+"/avatar")
	r.Header.Set("X-User-ID", "someone-else")

	w := do(t, router, r)

	require.Equal(t, http.StatusForbidden, w.Code)
	assert.Contains(t, w.Body.String(), "You can only delete your own avatars")
}
