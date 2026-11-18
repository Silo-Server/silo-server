package apiv2

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/adminjob"
	"github.com/Silo-Server/silo-server/internal/models"
)

type fakeLibraryJobs struct{ job *models.AdminJob }

func (f *fakeLibraryJobs) GetByID(_ context.Context, id string) (*models.AdminJob, error) {
	if f.job == nil || f.job.ID != id {
		return nil, adminjob.ErrJobNotFound
	}
	return f.job, nil
}
func (f *fakeLibraryJobs) RequestCancellation(ctx context.Context, id string) (*models.AdminJob, error) {
	job, err := f.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if job.Status == adminjob.StatusCancelled {
		return job, nil
	}
	if job.JobType != adminjob.JobTypeLibraryRefresh || job.Status == adminjob.StatusCompleted || job.Status == adminjob.StatusFailed {
		return nil, adminjob.ErrJobNotCancellable
	}
	job.CancelRequested = true
	return job, nil
}
