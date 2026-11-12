package handlers

import (
	"context"
	"time"

	"github.com/Silo-Server/silo-server/internal/autoscan"
)

type fakeAutoscanStore struct {
	getSettingsFn      func() (autoscan.Settings, error)
	updateSettingsFn   func(autoscan.Settings) (autoscan.Settings, error)
	listConnectionsFn  func() ([]autoscan.Connection, error)
	createConnectionFn func(autoscan.Connection) (autoscan.Connection, error)
	updateConnectionFn func(autoscan.Connection) (autoscan.Connection, error)
	deleteConnectionFn func(string) error
	listSourcesFn      func() ([]autoscan.Source, error)
	getSourceFn        func(string) (autoscan.Source, error)
	createSourceFn     func(autoscan.Source) (autoscan.Source, error)
	updateSourceFn     func(autoscan.Source) (autoscan.Source, error)
	deleteSourceFn     func(string) error
	listScansFn        func(autoscan.ScanListFilter) ([]autoscan.ScanWithEvent, error)
	countScansFn       func(autoscan.ScanListFilter) (int, error)
	listEventsFn       func(autoscan.EventListFilter) ([]autoscan.EventWithRuns, error)
	countEventsFn      func(autoscan.EventListFilter) (int, error)
	listRunningFn      func() ([]autoscan.Event, error)
	queueSummaryFn     func() (autoscan.QueueSummary, error)
	latestEventAtFn    func() (*time.Time, error)

	createWebhookFn func(string) (autoscan.WebhookEndpoint, string, error)
	rotateWebhookFn func(string) (autoscan.WebhookEndpoint, string, error)
	deleteWebhookFn func(string) error
	getWebhookFn    func(string) (autoscan.WebhookEndpoint, error)
	listWebhooksFn  func() ([]autoscan.WebhookEndpoint, error)
	revealTokenFn   func(string) (string, error)
	resolveTokenFn  func(string) (autoscan.Source, autoscan.WebhookEndpoint, error)
	touchedSources  []string
	webhookErrs     map[string]string
}

func (f *fakeAutoscanStore) GetSettings(context.Context) (autoscan.Settings, error) {
	if f.getSettingsFn != nil {
		return f.getSettingsFn()
	}
	return autoscan.Settings{}, nil
}

func (f *fakeAutoscanStore) UpdateSettings(_ context.Context, s autoscan.Settings) (autoscan.Settings, error) {
	if f.updateSettingsFn != nil {
		return f.updateSettingsFn(s)
	}
	return s, nil
}

func (f *fakeAutoscanStore) ListConnections(context.Context) ([]autoscan.Connection, error) {
	if f.listConnectionsFn != nil {
		return f.listConnectionsFn()
	}
	return nil, nil
}

func (f *fakeAutoscanStore) CreateConnection(_ context.Context, c autoscan.Connection) (autoscan.Connection, error) {
	if f.createConnectionFn != nil {
		return f.createConnectionFn(c)
	}
	return c, nil
}

func (f *fakeAutoscanStore) UpdateConnection(_ context.Context, c autoscan.Connection) (autoscan.Connection, error) {
	if f.updateConnectionFn != nil {
		return f.updateConnectionFn(c)
	}
	return c, nil
}

func (f *fakeAutoscanStore) DeleteConnection(_ context.Context, id string) error {
	if f.deleteConnectionFn != nil {
		return f.deleteConnectionFn(id)
	}
	return nil
}

func (f *fakeAutoscanStore) ListSources(context.Context) ([]autoscan.Source, error) {
	if f.listSourcesFn != nil {
		return f.listSourcesFn()
	}
	return nil, nil
}

func (f *fakeAutoscanStore) GetSource(_ context.Context, id string) (autoscan.Source, error) {
	if f.getSourceFn != nil {
		return f.getSourceFn(id)
	}
	return autoscan.Source{ID: id}, nil
}

func (f *fakeAutoscanStore) CreateSource(_ context.Context, s autoscan.Source) (autoscan.Source, error) {
	if f.createSourceFn != nil {
		return f.createSourceFn(s)
	}
	return s, nil
}

func (f *fakeAutoscanStore) UpdateSource(_ context.Context, s autoscan.Source) (autoscan.Source, error) {
	if f.updateSourceFn != nil {
		return f.updateSourceFn(s)
	}
	return s, nil
}

func (f *fakeAutoscanStore) DeleteSource(_ context.Context, id string) error {
	if f.deleteSourceFn != nil {
		return f.deleteSourceFn(id)
	}
	return nil
}

func (f *fakeAutoscanStore) ListAutoscanScans(_ context.Context, filter autoscan.ScanListFilter) ([]autoscan.ScanWithEvent, error) {
	if f.listScansFn != nil {
		return f.listScansFn(filter)
	}
	return nil, nil
}

func (f *fakeAutoscanStore) CountAutoscanScans(_ context.Context, filter autoscan.ScanListFilter) (int, error) {
	if f.countScansFn != nil {
		return f.countScansFn(filter)
	}
	return 0, nil
}

func (f *fakeAutoscanStore) ListEvents(_ context.Context, filter autoscan.EventListFilter) ([]autoscan.EventWithRuns, error) {
	if f.listEventsFn != nil {
		return f.listEventsFn(filter)
	}
	return nil, nil
}

func (f *fakeAutoscanStore) CountEvents(_ context.Context, filter autoscan.EventListFilter) (int, error) {
	if f.countEventsFn != nil {
		return f.countEventsFn(filter)
	}
	return 0, nil
}

func (f *fakeAutoscanStore) ListRunningEvents(context.Context) ([]autoscan.Event, error) {
	if f.listRunningFn != nil {
		return f.listRunningFn()
	}
	return nil, nil
}

func (f *fakeAutoscanStore) GetQueueSummary(context.Context) (autoscan.QueueSummary, error) {
	if f.queueSummaryFn != nil {
		return f.queueSummaryFn()
	}
	return autoscan.QueueSummary{}, nil
}

func (f *fakeAutoscanStore) LatestEventAt(context.Context) (*time.Time, error) {
	if f.latestEventAtFn != nil {
		return f.latestEventAtFn()
	}
	return nil, nil
}

func (f *fakeAutoscanStore) CreateWebhookEndpoint(_ context.Context, sourceID string) (autoscan.WebhookEndpoint, string, error) {
	if f.createWebhookFn != nil {
		return f.createWebhookFn(sourceID)
	}
	return autoscan.WebhookEndpoint{SourceID: sourceID}, "", nil
}

func (f *fakeAutoscanStore) RotateWebhookEndpoint(_ context.Context, sourceID string) (autoscan.WebhookEndpoint, string, error) {
	if f.rotateWebhookFn != nil {
		return f.rotateWebhookFn(sourceID)
	}
	return autoscan.WebhookEndpoint{SourceID: sourceID}, "", nil
}

func (f *fakeAutoscanStore) DeleteWebhookEndpoint(_ context.Context, sourceID string) error {
	if f.deleteWebhookFn != nil {
		return f.deleteWebhookFn(sourceID)
	}
	return nil
}

func (f *fakeAutoscanStore) GetWebhookEndpoint(_ context.Context, sourceID string) (autoscan.WebhookEndpoint, error) {
	if f.getWebhookFn != nil {
		return f.getWebhookFn(sourceID)
	}
	return autoscan.WebhookEndpoint{}, autoscan.ErrNotFound
}

func (f *fakeAutoscanStore) ListWebhookEndpoints(context.Context) ([]autoscan.WebhookEndpoint, error) {
	if f.listWebhooksFn != nil {
		return f.listWebhooksFn()
	}
	return nil, nil
}

func (f *fakeAutoscanStore) RevealWebhookToken(_ context.Context, sourceID string) (string, error) {
	if f.revealTokenFn != nil {
		return f.revealTokenFn(sourceID)
	}
	return "", autoscan.ErrNotFound
}

func (f *fakeAutoscanStore) ResolveWebhookToken(_ context.Context, token string) (autoscan.Source, autoscan.WebhookEndpoint, error) {
	if f.resolveTokenFn != nil {
		return f.resolveTokenFn(token)
	}
	return autoscan.Source{}, autoscan.WebhookEndpoint{}, autoscan.ErrNotFound
}

func (f *fakeAutoscanStore) TouchWebhookReceived(_ context.Context, sourceID string) error {
	f.touchedSources = append(f.touchedSources, sourceID)
	return nil
}

func (f *fakeAutoscanStore) RecordWebhookError(_ context.Context, sourceID, msg string) error {
	if f.webhookErrs == nil {
		f.webhookErrs = map[string]string{}
	}
	f.webhookErrs[sourceID] = msg
	return nil
}

type fakeAutoscanTriggerer struct {
	called bool
	err    error
	// available is returned by ListAvailableScanSources (the Add-source picker /
	// create-validation set).
	available []autoscan.AvailableScanSource
	// availableErr, when set, fails ListAvailableScanSources.
	availableErr error
	// testResult / testErr drive TestConnection + TestConnectionByID.
	testResult autoscan.ConnectionTestResult
	testErr    error
	// suggestions / suggestErr drive SuggestRewrites.
	suggestions autoscan.RewriteSuggestions
	suggestErr  error
	// done, when non-nil, receives once PollOnce runs. HandleTrigger dispatches
	// PollOnce on a detached goroutine, so tests synchronize on this instead of
	// reading `called` straight after the handler returns (which races the
	// goroutine and is also an unsynchronized read of `called`). The channel
	// send happens-before the test's receive, so reading `called` afterwards is
	// race-free.
	done chan struct{}
	// ingested records IngestChanges calls; ingestResult/ingestErr drive the
	// response.
	ingested     []autoscan.ChangeIngest
	ingestResult autoscan.IngestResult
	ingestErr    error
}

func (f *fakeAutoscanTriggerer) PollOnce(context.Context) error {
	f.called = true
	if f.done != nil {
		f.done <- struct{}{}
	}
	return f.err
}

func (f *fakeAutoscanTriggerer) ListAvailableScanSources(context.Context) ([]autoscan.AvailableScanSource, error) {
	if f.availableErr != nil {
		return nil, f.availableErr
	}
	return f.available, nil
}

func (f *fakeAutoscanTriggerer) TestConnection(context.Context, autoscan.Connection) (autoscan.ConnectionTestResult, error) {
	return f.testResult, f.testErr
}

func (f *fakeAutoscanTriggerer) TestConnectionByID(context.Context, string) (autoscan.ConnectionTestResult, error) {
	return f.testResult, f.testErr
}

func (f *fakeAutoscanTriggerer) SuggestRewrites(context.Context, string) (autoscan.RewriteSuggestions, error) {
	return f.suggestions, f.suggestErr
}

func (f *fakeAutoscanTriggerer) IngestChanges(_ context.Context, in autoscan.ChangeIngest) (autoscan.IngestResult, error) {
	f.ingested = append(f.ingested, in)
	return f.ingestResult, f.ingestErr
}
