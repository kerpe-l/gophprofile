package domain_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/kerpe-l/gophprofile/internal/domain"
)

func TestUploadStatusAllowedFrom(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		to   domain.UploadStatus
		want []domain.UploadStatus
	}{
		{
			name: "uploaded follows uploading",
			to:   domain.UploadStatusUploaded,
			want: []domain.UploadStatus{domain.UploadStatusUploading},
		},
		{
			name: "failed follows uploading",
			to:   domain.UploadStatusFailed,
			want: []domain.UploadStatus{domain.UploadStatusUploading},
		},
		{
			name: "uploading is never entered again",
			to:   domain.UploadStatusUploading,
			want: nil,
		},
		{
			name: "unknown status has no predecessors",
			to:   domain.UploadStatus("bogus"),
			want: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, tc.to.AllowedFrom())
		})
	}
}

func TestProcessingStatusAllowedFrom(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		to   domain.ProcessingStatus
		want []domain.ProcessingStatus
	}{
		{
			name: "processing starts and restarts",
			to:   domain.ProcessingStatusProcessing,
			want: []domain.ProcessingStatus{domain.ProcessingStatusPending, domain.ProcessingStatusProcessing},
		},
		{
			name: "completed follows started processing only",
			to:   domain.ProcessingStatusCompleted,
			want: []domain.ProcessingStatus{domain.ProcessingStatusProcessing},
		},
		{
			name: "failed is reachable before and during processing",
			to:   domain.ProcessingStatusFailed,
			want: []domain.ProcessingStatus{domain.ProcessingStatusPending, domain.ProcessingStatusProcessing},
		},
		{
			name: "pending is never entered again",
			to:   domain.ProcessingStatusPending,
			want: nil,
		},
		{
			name: "unknown status has no predecessors",
			to:   domain.ProcessingStatus("bogus"),
			want: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, tc.to.AllowedFrom())
		})
	}
}

// Терминальные статусы никуда не ведут: не должно найтись перехода,
// который возвращает запись из completed или failed.
func TestTerminalStatusesHaveNoSuccessors(t *testing.T) {
	t.Parallel()

	uploadStatuses := []domain.UploadStatus{
		domain.UploadStatusUploading, domain.UploadStatusUploaded, domain.UploadStatusFailed,
	}
	for _, to := range uploadStatuses {
		assert.NotContainsf(t, to.AllowedFrom(), domain.UploadStatusUploaded, "переход в %s", to)
		assert.NotContainsf(t, to.AllowedFrom(), domain.UploadStatusFailed, "переход в %s", to)
	}

	processingStatuses := []domain.ProcessingStatus{
		domain.ProcessingStatusPending, domain.ProcessingStatusProcessing,
		domain.ProcessingStatusCompleted, domain.ProcessingStatusFailed,
	}
	for _, to := range processingStatuses {
		assert.NotContainsf(t, to.AllowedFrom(), domain.ProcessingStatusCompleted, "переход в %s", to)
		assert.NotContainsf(t, to.AllowedFrom(), domain.ProcessingStatusFailed, "переход в %s", to)
	}
}
