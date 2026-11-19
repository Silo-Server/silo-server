package requests

import (
	"context"
)

type recordedApproval struct {
	requestID string
	origin    ApprovalOrigin
}

type recordingLifecycleNotifier struct {
	submitted []string
	approved  []recordedApproval
	declined  []string
}

func (n *recordingLifecycleNotifier) RequestSubmitted(_ context.Context, req Request) {
	n.submitted = append(n.submitted, req.ID)
}

func (n *recordingLifecycleNotifier) RequestApproved(_ context.Context, req Request, origin ApprovalOrigin) {
	n.approved = append(n.approved, recordedApproval{requestID: req.ID, origin: origin})
}

func (n *recordingLifecycleNotifier) RequestDeclined(_ context.Context, req Request) {
	n.declined = append(n.declined, req.ID)
}

type fakeNotifier struct {
	requestIDs []string
	contentIDs []string
	followers  [][]Follower
	announced  []string
	err        error
}

func (f *fakeNotifier) AnnounceFulfilled(_ context.Context, req Request) {
	f.announced = append(f.announced, req.ID)
}

func (f *fakeNotifier) NotifyFulfilled(_ context.Context, req Request, contentID string) error {
	if f.err != nil {
		return f.err
	}
	f.requestIDs = append(f.requestIDs, req.ID)
	f.contentIDs = append(f.contentIDs, contentID)
	f.followers = append(f.followers, req.Followers)
	return nil
}

func completedRequestFixture(id string, tmdbID int) *Request {
	return &Request{
		ID:                   id,
		MediaType:            MediaTypeMovie,
		TMDBID:               tmdbID,
		Title:                "Fixture Movie",
		Status:               StatusCompleted,
		Outcome:              OutcomeActive,
		RequestedByUserID:    7,
		RequestedByProfileID: "profile-1",
	}
}
