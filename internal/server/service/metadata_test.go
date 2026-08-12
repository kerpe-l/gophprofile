package service_test

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kerpe-l/gophprofile/internal/domain"
)

func TestMetadata(t *testing.T) {
	t.Parallel()

	d := newDeps()
	d.repo.avatar = storedAvatar()
	svc := newService(t, d)

	avatar, err := svc.Metadata(t.Context(), d.repo.avatar.ID)
	require.NoError(t, err)

	assert.Equal(t, d.repo.avatar, avatar)
}

func TestMetadataNotFound(t *testing.T) {
	t.Parallel()

	d := newDeps()
	d.repo.getErr = domain.ErrNotFound
	svc := newService(t, d)

	_, err := svc.Metadata(t.Context(), uuid.New())
	require.ErrorIs(t, err, domain.ErrNotFound)
}

func TestListByUser(t *testing.T) {
	t.Parallel()

	d := newDeps()
	d.repo.list = []domain.Avatar{storedAvatar(), storedAvatar()}
	svc := newService(t, d)

	avatars, err := svc.ListByUser(t.Context(), testUserID)
	require.NoError(t, err)

	assert.Equal(t, d.repo.list, avatars, "порядок перебора сохраняется")
}

func TestListByUserEmpty(t *testing.T) {
	t.Parallel()

	d := newDeps()
	svc := newService(t, d)

	avatars, err := svc.ListByUser(t.Context(), testUserID)
	require.NoError(t, err)

	assert.Empty(t, avatars)
}

func TestListByUserFails(t *testing.T) {
	t.Parallel()

	d := newDeps()
	d.repo.list = []domain.Avatar{storedAvatar()}
	d.repo.listErr = errors.New("database is unavailable")
	svc := newService(t, d)

	avatars, err := svc.ListByUser(t.Context(), testUserID)
	require.ErrorIs(t, err, d.repo.listErr)

	assert.Nil(t, avatars)
}
