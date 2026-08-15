# Auto-Approval Notification Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Preserve automatic approval lifecycle and server-channel notifications while suppressing the redundant personal `request.approved` delivery to the requester.

**Architecture:** Carry approval origin as typed metadata through the request lifecycle notifier boundary. The request service selects manual versus automatic origin, while the notifications package remains responsible for destinations and suppresses only the personal delivery for automatic approval.

**Tech Stack:** Go 1.26.4, standard-library `context`, `log/slog`, and `testing`

## Global Constraints

- Commands assume the repository root is the current working directory.
- Preserve the additive-only `/api/v1` contract; this change is internal and adds no API fields.
- Do not change request lifecycle persistence, fulfillment delivery, or server-channel approval behavior.
- Unknown approval-origin values retain the existing manual behavior and send a personal notification.
- The managed Codex worktree may block Git index writes; if it does, skip commit commands and report the limitation rather than altering Git metadata manually.

---

### Task 1: Carry Approval Origin Through the Request Lifecycle Contract

**Files:**
- Modify: `internal/requests/notify.go`
- Modify: `internal/requests/notify_test.go`
- Modify: `internal/requests/service.go`
- Modify: `internal/notifications/request_notifier.go`

**Interfaces:**
- Produces: `type ApprovalOrigin string`
- Produces: `ApprovalOriginManual ApprovalOrigin`
- Produces: `ApprovalOriginAutomatic ApprovalOrigin`
- Changes: `LifecycleNotifier.RequestApproved(context.Context, Request, ApprovalOrigin)`
- Produces: `(*Service).notifyApproval(context.Context, Request, ApprovalOrigin)`

- [ ] **Step 1: Write request-service tests that name the origin of each approval path**

Append lifecycle recording support and two focused tests to `internal/requests/notify_test.go`:

~~~go
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

func TestCreateRequestAutoApprovalReportsAutomaticOrigin(t *testing.T) {
	store := newFakeStore()
	store.settings.RequestsEnabled = true
	store.settings.GlobalAutoApprovalEnabled = true
	store.integrations = []Integration{autoApproveRouterInst("router-1", "radarr-key")}
	service := newTestService(store)
	service.SetRouterProvider(&fakeRouterProvider{})
	recorder := &recordingLifecycleNotifier{}
	service.SetLifecycleNotifier(recorder)

	req, err := service.CreateRequest(context.Background(), testViewer(1), CreateRequestInput{
		MediaType: MediaTypeMovie,
		TMDBID:    550,
		Title:     "Fight Club",
	})
	if err != nil {
		t.Fatalf("CreateRequest returned error: %v", err)
	}
	if len(recorder.submitted) != 1 || recorder.submitted[0] != req.ID {
		t.Fatalf("submitted notifications = %v, want [%s]", recorder.submitted, req.ID)
	}
	want := []recordedApproval{{requestID: req.ID, origin: ApprovalOriginAutomatic}}
	if !reflect.DeepEqual(recorder.approved, want) {
		t.Fatalf("approved notifications = %+v, want %+v", recorder.approved, want)
	}
}

func TestApproveReportsManualOrigin(t *testing.T) {
	store := newFakeStore()
	store.integrations = []Integration{routerInst("router-1")}
	store.requests["req-1"] = &Request{
		ID:                   "req-1",
		MediaType:            MediaTypeMovie,
		TMDBID:               550,
		Title:                "Fight Club",
		Status:               StatusPending,
		Outcome:              OutcomeActive,
		RequestedByUserID:    7,
		RequestedByProfileID: "profile-7",
	}
	service := newTestService(store)
	service.SetRouterProvider(&fakeRouterProvider{})
	recorder := &recordingLifecycleNotifier{}
	service.SetLifecycleNotifier(recorder)

	_, err := service.Approve(context.Background(), Viewer{UserID: 99, IsAdmin: true}, "req-1")
	if err != nil {
		t.Fatalf("Approve returned error: %v", err)
	}
	want := []recordedApproval{{requestID: "req-1", origin: ApprovalOriginManual}}
	if !reflect.DeepEqual(recorder.approved, want) {
		t.Fatalf("approved notifications = %+v, want %+v", recorder.approved, want)
	}
}
~~~

Add `reflect` to the existing test imports. The production mutation these tests catch is either approval path passing the wrong origin or reverting both paths to indistinguishable approval calls.

- [ ] **Step 2: Run the focused tests and verify RED**

Run:

~~~bash
go test ./internal/requests -run 'Test(CreateRequestAutoApprovalReportsAutomaticOrigin|ApproveReportsManualOrigin)$'
~~~

Expected: FAIL to compile because `ApprovalOrigin`, its constants, and the new `RequestApproved` signature do not exist.

- [ ] **Step 3: Add the typed origin and approval helper**

In `internal/requests/notify.go`, add the origin type immediately before `LifecycleNotifier`:

~~~go
// ApprovalOrigin identifies how an approval transition was initiated so
// notification destinations can distinguish policy from administrator action.
type ApprovalOrigin string

const (
	ApprovalOriginManual    ApprovalOrigin = "manual"
	ApprovalOriginAutomatic ApprovalOrigin = "automatic"
)
~~~

Change the interface method to:

~~~go
RequestApproved(ctx context.Context, req Request, origin ApprovalOrigin)
~~~

Add this helper after `notifyLifecycle`:

~~~go
func (s *Service) notifyApproval(ctx context.Context, req Request, origin ApprovalOrigin) {
	s.notifyLifecycle(ctx, req, func(notifier LifecycleNotifier, ctx context.Context, req Request) {
		notifier.RequestApproved(ctx, req, origin)
	})
}
~~~

- [ ] **Step 4: Pass the correct origin from automatic and manual service paths**

In `internal/requests/service.go`, replace the automatic approval call with:

~~~go
s.notifyApproval(ctx, *req, ApprovalOriginAutomatic)
~~~

Replace the administrator approval call with:

~~~go
s.notifyApproval(ctx, *approved, ApprovalOriginManual)
~~~

Keep the automatic-path comment explicit that server channels still see the transition.

- [ ] **Step 5: Keep the concrete notification adapter compatible without changing behavior yet**

In `internal/notifications/request_notifier.go`, change the method signature to accept the origin while still dispatching the personal delivery:

~~~go
func (n *RequestLifecycleNotifier) RequestApproved(
	ctx context.Context,
	req requests.Request,
	_ requests.ApprovalOrigin,
) {
	n.system.PostServerChannelRequestEvent(ctx, ServerChannelEventRequestApproved, requestEventInfoFor(req))
	n.dispatchPersonal(ctx, req, DeliveryTypeRequestApproved)
}
~~~

This keeps all packages compiling while leaving the issue demonstrably unfixed until Task 2's notification-boundary test is red.

- [ ] **Step 6: Format and verify GREEN for request-origin propagation**

Run:

~~~bash
gofmt -w internal/requests/notify.go internal/requests/notify_test.go internal/requests/service.go internal/notifications/request_notifier.go
go test ./internal/requests -run 'Test(CreateRequestAutoApprovalReportsAutomaticOrigin|ApproveReportsManualOrigin)$'
~~~

Expected: PASS.

- [ ] **Step 7: Commit the lifecycle-contract change**

Run when Git index writes are available:

~~~bash
git add internal/requests/notify.go internal/requests/notify_test.go internal/requests/service.go internal/notifications/request_notifier.go
git commit -m "refactor(requests): identify approval notification origin"
~~~

---

### Task 2: Suppress Only Automatic Personal Approval Deliveries

**Files:**
- Create: `internal/notifications/request_notifier_test.go`
- Modify: `internal/notifications/request_notifier.go`
- Modify: `internal/api/router.go`

**Interfaces:**
- Produces internally: `requestLifecycleBackend`
- Produces internally: `newRequestLifecycleNotifier(requestLifecycleBackend, *slog.Logger)`
- Consumes: `requests.ApprovalOriginAutomatic`
- Preserves: `ServerChannelEventRequestApproved` for every origin
- Preserves: `DeliveryTypeRequestApproved` for manual and unknown origins

- [ ] **Step 1: Write notification-boundary tests for automatic, manual, and unknown origins**

Create `internal/notifications/request_notifier_test.go`:

~~~go
package notifications

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/requests"
)

type recordedServerRequestEvent struct {
	event string
	info  RequestEventInfo
}

type recordingRequestLifecycleBackend struct {
	serverEvents       []recordedServerRequestEvent
	personalDeliveries chan string
}

func newRecordingRequestLifecycleBackend() *recordingRequestLifecycleBackend {
	return &recordingRequestLifecycleBackend{personalDeliveries: make(chan string, 1)}
}

func (b *recordingRequestLifecycleBackend) PostServerChannelRequestEvent(
	_ context.Context,
	event string,
	info RequestEventInfo,
) {
	b.serverEvents = append(b.serverEvents, recordedServerRequestEvent{event: event, info: info})
}

func (b *recordingRequestLifecycleBackend) dispatchRequestLifecycle(
	_ context.Context,
	_ requests.Request,
	deliveryType string,
) error {
	b.personalDeliveries <- deliveryType
	return nil
}

func requestApprovalFixture() requests.Request {
	return requests.Request{
		ID:                   "req-1",
		MediaType:            requests.MediaTypeMovie,
		TMDBID:               550,
		Title:                "Fight Club",
		RequestedByUserID:    7,
		RequestedByProfileID: "profile-7",
	}
}

func newTestRequestLifecycleNotifier(backend requestLifecycleBackend) *RequestLifecycleNotifier {
	return newRequestLifecycleNotifier(backend, slog.New(slog.DiscardHandler))
}

func assertApprovalServerEvent(t *testing.T, backend *recordingRequestLifecycleBackend) {
	t.Helper()
	if len(backend.serverEvents) != 1 {
		t.Fatalf("server events = %+v, want one approval event", backend.serverEvents)
	}
	got := backend.serverEvents[0]
	if got.event != ServerChannelEventRequestApproved || got.info.RequestID != "req-1" {
		t.Fatalf("server event = %+v, want request.approved for req-1", got)
	}
}

func TestRequestApprovedAutomaticKeepsServerEventAndSkipsPersonalDelivery(t *testing.T) {
	backend := newRecordingRequestLifecycleBackend()
	notifier := newTestRequestLifecycleNotifier(backend)

	notifier.RequestApproved(context.Background(), requestApprovalFixture(), requests.ApprovalOriginAutomatic)

	assertApprovalServerEvent(t, backend)
	select {
	case deliveryType := <-backend.personalDeliveries:
		t.Fatalf("personal delivery = %q, want none", deliveryType)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestRequestApprovedManualKeepsServerAndPersonalDelivery(t *testing.T) {
	backend := newRecordingRequestLifecycleBackend()
	notifier := newTestRequestLifecycleNotifier(backend)

	notifier.RequestApproved(context.Background(), requestApprovalFixture(), requests.ApprovalOriginManual)

	assertApprovalServerEvent(t, backend)
	select {
	case deliveryType := <-backend.personalDeliveries:
		if deliveryType != DeliveryTypeRequestApproved {
			t.Fatalf("personal delivery = %q, want %q", deliveryType, DeliveryTypeRequestApproved)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for manual approval personal delivery")
	}
}

func TestRequestApprovedUnknownOriginRetainsPersonalDelivery(t *testing.T) {
	backend := newRecordingRequestLifecycleBackend()
	notifier := newTestRequestLifecycleNotifier(backend)

	notifier.RequestApproved(context.Background(), requestApprovalFixture(), requests.ApprovalOrigin("future"))

	select {
	case deliveryType := <-backend.personalDeliveries:
		if deliveryType != DeliveryTypeRequestApproved {
			t.Fatalf("personal delivery = %q, want %q", deliveryType, DeliveryTypeRequestApproved)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for conservative personal delivery")
	}
}
~~~

These tests exercise the real lifecycle adapter. The backend double is specific to its two destination boundaries and records complete `RequestEventInfo` input rather than replacing notifier behavior.

- [ ] **Step 2: Run the notification tests and verify RED**

Run:

~~~bash
go test ./internal/notifications -run '^TestRequestApproved'
~~~

Expected: FAIL to compile because `requestLifecycleBackend` and `newRequestLifecycleNotifier` do not exist. If those are introduced during test setup, the automatic case must fail by receiving `request.approved` on `personalDeliveries`.

- [ ] **Step 3: Narrow the adapter dependency to its actual backend operations**

In `internal/notifications/request_notifier.go`, add `log/slog` to imports and define:

~~~go
type requestLifecycleBackend interface {
	PostServerChannelRequestEvent(ctx context.Context, event string, info RequestEventInfo)
	dispatchRequestLifecycle(ctx context.Context, req requests.Request, deliveryType string) error
}
~~~

Change `RequestLifecycleNotifier` and its constructors to:

~~~go
type RequestLifecycleNotifier struct {
	backend requestLifecycleBackend
	logger  *slog.Logger
}

func NewRequestLifecycleNotifier(system *System) *RequestLifecycleNotifier {
	if system == nil {
		return nil
	}
	return newRequestLifecycleNotifier(system, system.logger)
}

func newRequestLifecycleNotifier(
	backend requestLifecycleBackend,
	logger *slog.Logger,
) *RequestLifecycleNotifier {
	return &RequestLifecycleNotifier{backend: backend, logger: logger}
}
~~~

Replace this file's `n.system.PostServerChannelRequestEvent` and `n.system.dispatchRequestLifecycle` uses with `n.backend` calls. Replace `n.system.logger.WarnContext` with `n.logger.WarnContext`. No methods are added to `System` for tests.

- [ ] **Step 4: Implement the origin-specific destination rule**

Change `RequestApproved` to:

~~~go
func (n *RequestLifecycleNotifier) RequestApproved(
	ctx context.Context,
	req requests.Request,
	origin requests.ApprovalOrigin,
) {
	n.backend.PostServerChannelRequestEvent(ctx, ServerChannelEventRequestApproved, requestEventInfoFor(req))
	if origin == requests.ApprovalOriginAutomatic {
		return
	}
	n.dispatchPersonal(ctx, req, DeliveryTypeRequestApproved)
}
~~~

Use the same `n.backend.PostServerChannelRequestEvent` substitution in `RequestSubmitted` and `RequestDeclined`. Update the delivery-type and adapter comments so they describe personal approval notifications as manual-only.

In `internal/api/router.go`, change the wiring comment from personal delivery "on approve" to "on manual approve". Do not alter wiring behavior.

- [ ] **Step 5: Format and verify GREEN for destination behavior**

Run:

~~~bash
gofmt -w internal/notifications/request_notifier.go internal/notifications/request_notifier_test.go internal/api/router.go
go test ./internal/notifications -run '^TestRequestApproved'
~~~

Expected: PASS for automatic, manual, and unknown origins.

- [ ] **Step 6: Run the mutation check**

Confirm mentally and through the focused tests:

- Removing the automatic-origin guard makes the automatic test fail.
- Reversing the guard makes the manual test fail.
- Moving the server-channel post below the guard makes the automatic test fail.
- Treating unknown origin as automatic makes the unknown-origin test fail.

- [ ] **Step 7: Commit the destination fix**

Run when Git index writes are available:

~~~bash
git add internal/notifications/request_notifier.go internal/notifications/request_notifier_test.go internal/api/router.go
git commit -m "fix(notifications): skip auto-approval requester notice"
~~~

---

### Task 3: Verify the Complete Fix

**Files:**
- Verify: `internal/requests/notify.go`
- Verify: `internal/requests/notify_test.go`
- Verify: `internal/requests/service.go`
- Verify: `internal/notifications/request_notifier.go`
- Verify: `internal/notifications/request_notifier_test.go`
- Verify: `internal/api/router.go`
- Verify: `docs/superpowers/specs/2026-08-15-auto-approval-notification-design.md`
- Verify: `docs/superpowers/plans/2026-08-15-auto-approval-notification.md`

**Interfaces:**
- Verifies: automatic approval records and broadcasts without personal delivery
- Verifies: manual approval still broadcasts and sends personal delivery
- Verifies: request fulfillment and decline paths remain unchanged

- [ ] **Step 1: Run focused package tests**

Run:

~~~bash
go test ./internal/requests ./internal/notifications ./internal/api
~~~

Expected: PASS.

- [ ] **Step 2: Run the whole Go suite**

Run:

~~~bash
make test-go
~~~

Expected: PASS, except for tests already carrying an in-source `t.Skip` with a documented reason.

- [ ] **Step 3: Run lint and repository hygiene checks**

Run:

~~~bash
make lint
make verify-local-paths
git diff --check
~~~

Expected: `make verify-local-paths` and `git diff --check` PASS. `make lint` may report known whole-tree baseline findings; confirm none point to changed lines.

- [ ] **Step 4: Inspect the final diff for scope and contract accuracy**

Run:

~~~bash
git status --short
git diff -- internal/requests/notify.go internal/requests/notify_test.go internal/requests/service.go internal/notifications/request_notifier.go internal/notifications/request_notifier_test.go internal/api/router.go docs/superpowers/specs/2026-08-15-auto-approval-notification-design.md docs/superpowers/plans/2026-08-15-auto-approval-notification.md
~~~

Confirm the diff contains no API, migration, frontend, fulfillment, or unrelated refactoring changes.

- [ ] **Step 5: Create a final fix commit if Task 1 and Task 2 could not be committed separately**

Run when Git index writes are available:

~~~bash
git add internal/requests/notify.go internal/requests/notify_test.go internal/requests/service.go internal/notifications/request_notifier.go internal/notifications/request_notifier_test.go internal/api/router.go docs/superpowers/specs/2026-08-15-auto-approval-notification-design.md docs/superpowers/plans/2026-08-15-auto-approval-notification.md
git commit -m "fix(notifications): skip auto-approval requester notice"
~~~
