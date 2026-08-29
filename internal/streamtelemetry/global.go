package streamtelemetry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/httpstream"
)

type PublisherRef struct {
	PublisherID string
	NodeID      string
}

type PublisherState string

const (
	PublisherFresh    PublisherState = "fresh"
	PublisherStale    PublisherState = "stale"
	PublisherDegraded PublisherState = "degraded"
	PublisherDeparted PublisherState = "departed"
)

type PublisherStatus struct {
	PublisherRef
	Epoch         int64
	Sequence      uint64
	LastHeartbeat time.Time
	CapturedAt    time.Time
	State         PublisherState
	DecodeErrors  int
	Truncated     bool
	Reason        string
}

type AttributedValue struct {
	Value      string
	Publishers []PublisherRef
}

type GlobalIdentityConflict struct {
	Field  string
	Values []AttributedValue
}

type AttributedIdentityConflict struct {
	Publisher PublisherRef
	Conflict  IdentityConflict
}

type PublisherValue struct {
	Publisher PublisherRef
	Value     string
}

type GlobalSessionView struct {
	Subject                     Subject
	ProfileID                   string
	SessionID                   string
	MediaFileID                 int
	StartedAt                   time.Time
	StartedAtSource             StartedAtSource
	StartedAtDegraded           bool
	ViewerBytesAccepted         int64
	RelayBytesAccepted          int64
	BytesDegraded               bool
	LastByteAccepted            time.Time
	LastObservationEnd          time.Time
	OpenObservations            int64
	RealtimeConnectionAlive     bool
	RequestCount                int64
	Routes                      []RouteActivityView
	RoutesOverflowed            bool
	ViewerIPs                   []string
	ViewerIPsOverflowed         bool
	DeviceIDs                   []string
	DeviceIDsOverflowed         bool
	ClientVariants              []ClientVariant
	ClientVariantsOverflowed    bool
	UserAgents                  []string
	UserAgentsOverflowed        bool
	MediaFileIDs                []int
	MediaFileIDsOverflowed      bool
	PlayMethods                 []string
	PlayMethodsOverflowed       bool
	TokenIssuedAts              []time.Time
	TokenIssuedAtsOverflowed    bool
	TokenIssuedAtSources        map[TokenIssuedAtSource]int64
	Outcomes                    map[httpstream.StreamOutcome]int64
	HasIdentityConflict         bool
	IdentityConflicts           []GlobalIdentityConflict
	LocalIdentityConflicts      []AttributedIdentityConflict
	IdentityConflictsOverflowed bool
	Publishers                  []PublisherRef
	ViewerEdgePublishers        []PublisherRef
	PerPublisherPlayMethods     []PublisherValue
}

type GlobalTransferView struct {
	TransferView
	Publisher PublisherRef
}

type GlobalMonitoringView struct {
	BuiltAt                  time.Time
	Epoch                    string
	Complete                 bool
	IncompleteReasons        []string
	Publishers               []PublisherStatus
	MissingPublishers        []PublisherRef
	Sessions                 []GlobalSessionView
	Transfers                []GlobalTransferView
	Truncated                bool
	DroppedObservations      int64
	DroppedBytes             int64
	UnattributedObservations int64
	UnattributedBytes        int64
	DecodeErrors             int
	ClockSkewSuspected       bool
}

type ViewParams struct {
	Freshness                      time.Duration
	MembershipTTL                  time.Duration
	MaxMergedSessions              int
	MaxMergedTransfers             int
	MaxViewerIPsPerSession         int
	MaxDeviceIDsPerSession         int
	MaxClientVariantsPerSession    int
	MaxUserAgentsPerSession        int
	MaxMediaFileIDsPerSession      int
	MaxPlayMethodsPerSession       int
	MaxTokenIssuedAtPerSession     int
	MaxRoutesPerSession            int
	MaxIdentityConflictsPerSession int
}

type errGlobalSnapshotStoreUnsupported struct{}

func (errGlobalSnapshotStoreUnsupported) Error() string {
	return "stream telemetry store does not support global snapshots"
}

func (r *Registry) viewParams() ViewParams {
	return ViewParams{Freshness: r.cfg.Freshness, MembershipTTL: r.cfg.MembershipTTL,
		MaxMergedSessions: r.cfg.MaxMergedSessions, MaxMergedTransfers: r.cfg.MaxMergedTransfers,
		MaxViewerIPsPerSession: r.cfg.MaxViewerIPsPerSession, MaxDeviceIDsPerSession: r.cfg.MaxDeviceIDsPerSession,
		MaxClientVariantsPerSession: r.cfg.MaxClientVariantsPerSession, MaxUserAgentsPerSession: r.cfg.MaxClientVariantsPerSession,
		MaxMediaFileIDsPerSession: r.cfg.MaxMediaFileIDsPerSession, MaxPlayMethodsPerSession: r.cfg.MaxPlayMethodsPerSession,
		MaxTokenIssuedAtPerSession: r.cfg.MaxTokenIssuedAtPerSession, MaxRoutesPerSession: r.cfg.MaxRoutesPerSession,
		MaxIdentityConflictsPerSession: r.cfg.MaxIdentityConflictsPerSession}
}

func (r *Registry) GlobalView(ctx context.Context) (GlobalMonitoringView, error) {
	if r == nil || !r.cfg.Enabled {
		return GlobalMonitoringView{}, nil
	}
	store, ok := r.store.(GlobalSnapshotStore)
	if !ok {
		return GlobalMonitoringView{}, errGlobalSnapshotStoreUnsupported{}
	}
	set, err := store.LoadAll(ctx)
	if err != nil {
		r.warnRateLimited("failed to load global stream telemetry", &r.lastPublishWarnUnixNano, "error", err)
		return GlobalMonitoringView{}, err
	}
	return BuildGlobalView(set, now(), r.viewParams()), nil
}

type publisherContribution struct {
	ref      PublisherRef
	snapshot Snapshot
}

type sessionContribution struct {
	ref  PublisherRef
	view SessionView
}

func BuildGlobalView(set PublisherSet, at time.Time, params ViewParams) GlobalMonitoringView {
	view := GlobalMonitoringView{BuiltAt: at, Complete: true, Truncated: set.Truncated}
	snapshots := make(map[string]Snapshot, len(set.Snapshots))
	for _, snapshot := range set.Snapshots {
		snapshots[snapshot.PublisherID] = snapshot
	}
	errorsByPublisher := make(map[string][]PublisherError)
	for _, problem := range set.Errors {
		errorsByPublisher[problem.PublisherID] = append(errorsByPublisher[problem.PublisherID], problem)
		view.DecodeErrors = saturatingInt(view.DecodeErrors, problem.DecodeErrors)
	}
	for publisherID := range errorsByPublisher {
		problems := errorsByPublisher[publisherID]
		sort.Slice(problems, func(i, j int) bool {
			if problems[i].Reason == problems[j].Reason {
				return problems[i].DecodeErrors < problems[j].DecodeErrors
			}
			return problems[i].Reason < problems[j].Reason
		})
		errorsByPublisher[publisherID] = problems
	}
	members := append([]Member(nil), set.Members...)
	sort.Slice(members, func(i, j int) bool { return members[i].PublisherID < members[j].PublisherID })
	contributions := make([]publisherContribution, 0, len(members))
	for _, member := range members {
		snapshot, hasSnapshot := snapshots[member.PublisherID]
		ref := PublisherRef{PublisherID: member.PublisherID, NodeID: snapshot.NodeID}
		status := PublisherStatus{PublisherRef: ref, LastHeartbeat: member.LastHeartbeat, CapturedAt: snapshot.CapturedAt,
			Epoch: snapshot.PublisherEpoch, Sequence: snapshot.Sequence, Truncated: snapshot.Truncated}
		// Skew is only detectable in one direction from a single sample. A
		// publisher whose clock runs AHEAD stamps a future time and is caught
		// here. One running BEHIND is indistinguishable from one that stopped
		// publishing: the roster score is the publisher's own CapturedAt, so
		// heartbeat and snapshot drift together and there is no independent
		// clock to compare against. Such a publisher is classified stale and its
		// sessions leave the merged view — the safe direction, since the
		// alternative is serving data that may be minutes old as current.
		// PublisherStatus carries Epoch and Sequence precisely so two successive
		// reads of the parity endpoint distinguish "behind but advancing" from
		// "stalled"; BuildGlobalView is a pure function of one sample and cannot.
		heartbeatAge := at.Sub(member.LastHeartbeat)
		if heartbeatAge < -params.Freshness {
			view.ClockSkewSuspected = true
		}
		if heartbeatAge > params.MembershipTTL {
			status.State = PublisherDeparted
			view.Publishers = append(view.Publishers, status)
			continue
		}
		problems := errorsByPublisher[member.PublisherID]
		unusable := false
		for _, problem := range problems {
			status.DecodeErrors = saturatingInt(status.DecodeErrors, problem.DecodeErrors)
			if status.Reason == "" {
				status.Reason = problem.Reason
			}
			if problem.Reason == publisherReasonOversized || problem.Reason == publisherReasonMetaMissing || problem.Reason == publisherReasonIdentityMismatch {
				unusable = true
			}
		}
		capturedAge := at.Sub(snapshot.CapturedAt)
		if capturedAge < -params.Freshness {
			view.ClockSkewSuspected = true
		}
		if !hasSnapshot || unusable || capturedAge > params.Freshness {
			status.State = PublisherStale
			view.MissingPublishers = append(view.MissingPublishers, ref)
			view.Publishers = append(view.Publishers, status)
			continue
		}
		status.State = PublisherFresh
		if snapshot.Truncated || status.DecodeErrors > 0 || status.Reason == publisherReasonCountMismatch || status.Reason == publisherReasonDecode {
			status.State = PublisherDegraded
		}
		view.Publishers = append(view.Publishers, status)
		contributions = append(contributions, publisherContribution{ref: ref, snapshot: snapshot})
	}
	sort.Slice(view.MissingPublishers, func(i, j int) bool { return refLess(view.MissingPublishers[i], view.MissingPublishers[j]) })
	if len(view.MissingPublishers) > 0 {
		addReason(&view, "missing_publisher")
	}
	if set.Truncated {
		addReason(&view, "truncated")
	}
	for _, status := range view.Publishers {
		if (status.State == PublisherFresh || status.State == PublisherDegraded) && status.Truncated {
			addReason(&view, "publisher_truncated")
		}
		if status.DecodeErrors > 0 || status.Reason == publisherReasonCountMismatch || status.Reason == publisherReasonDecode {
			addReason(&view, "decode_errors")
		}
	}
	mergeContributions(&view, contributions, params)
	view.Epoch = globalEpoch(contributions)
	view.Complete = len(view.IncompleteReasons) == 0
	return view
}

func addReason(view *GlobalMonitoringView, reason string) {
	for _, existing := range view.IncompleteReasons {
		if existing == reason {
			return
		}
	}
	view.IncompleteReasons = append(view.IncompleteReasons, reason)
	sort.Strings(view.IncompleteReasons)
}

func mergeContributions(view *GlobalMonitoringView, contributions []publisherContribution, params ViewParams) {
	sort.Slice(contributions, func(i, j int) bool { return refLess(contributions[i].ref, contributions[j].ref) })
	bySession := make(map[string][]sessionContribution)
	for _, contribution := range contributions {
		view.DroppedObservations = saturatingAdd(view.DroppedObservations, contribution.snapshot.DroppedObservations)
		view.DroppedBytes = saturatingAdd(view.DroppedBytes, contribution.snapshot.DroppedBytes)
		view.UnattributedObservations = saturatingAdd(view.UnattributedObservations, contribution.snapshot.UnattributedObservations)
		view.UnattributedBytes = saturatingAdd(view.UnattributedBytes, contribution.snapshot.UnattributedBytes)
		view.Truncated = view.Truncated || contribution.snapshot.Truncated
		for _, session := range contribution.snapshot.Sessions {
			bySession[session.SessionID] = append(bySession[session.SessionID], sessionContribution{ref: contribution.ref, view: session})
		}
		for _, transfer := range contribution.snapshot.Transfers {
			view.Transfers = append(view.Transfers, GlobalTransferView{TransferView: cloneTransfer(transfer), Publisher: contribution.ref})
		}
	}
	ids := make([]string, 0, len(bySession))
	for id := range bySession {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if params.MaxMergedSessions > 0 && len(ids) > params.MaxMergedSessions {
		ids = ids[:params.MaxMergedSessions]
		view.Truncated = true
		addReason(view, "truncated")
	}
	for _, id := range ids {
		view.Sessions = append(view.Sessions, mergeSession(id, bySession[id], params))
	}
	sort.Slice(view.Transfers, func(i, j int) bool {
		if view.Transfers[i].Publisher.PublisherID == view.Transfers[j].Publisher.PublisherID {
			return view.Transfers[i].ID < view.Transfers[j].ID
		}
		return refLess(view.Transfers[i].Publisher, view.Transfers[j].Publisher)
	})
	if params.MaxMergedTransfers > 0 && len(view.Transfers) > params.MaxMergedTransfers {
		view.Transfers = view.Transfers[:params.MaxMergedTransfers]
		view.Truncated = true
		addReason(view, "truncated")
	}
}

func mergeSession(id string, contributions []sessionContribution, params ViewParams) GlobalSessionView {
	result := GlobalSessionView{SessionID: id, TokenIssuedAtSources: make(map[TokenIssuedAtSource]int64), Outcomes: make(map[httpstream.StreamOutcome]int64)}
	sort.Slice(contributions, func(i, j int) bool { return refLess(contributions[i].ref, contributions[j].ref) })
	viewerIPs, deviceIDs, userAgents, playMethods := map[string]struct{}{}, map[string]struct{}{}, map[string]struct{}{}, map[string]struct{}{}
	mediaFileIDs, tokenTimes := map[int]struct{}{}, map[int64]struct{}{}
	clients := map[ClientVariant]struct{}{}
	routes := map[string]RouteActivityView{}
	subjectValues, profileValues, mediaValues := map[string][]PublisherRef{}, map[string][]PublisherRef{}, map[string][]PublisherRef{}
	winningRank := 0
	winningTimes := map[int64]struct{}{}
	hasViewerEdge := false
	for _, contribution := range contributions {
		for _, route := range contribution.view.Routes {
			if route.Role == RoleViewerEgress {
				hasViewerEdge = true
				break
			}
		}
		if hasViewerEdge {
			break
		}
	}
	for _, contribution := range contributions {
		session, ref := contribution.view, contribution.ref
		result.Publishers = append(result.Publishers, ref)
		viewerEdge := false
		for _, route := range session.Routes {
			if route.Role == RoleViewerEgress {
				viewerEdge = true
				break
			}
		}
		if viewerEdge {
			result.ViewerEdgePublishers = append(result.ViewerEdgePublishers, ref)
			if session.Subject.Kind != "" && session.Subject.ID != "" {
				key := string(session.Subject.Kind) + "\x00" + session.Subject.ID
				subjectValues[key] = append(subjectValues[key], ref)
			}
			if session.ProfileID != "" {
				profileValues[session.ProfileID] = append(profileValues[session.ProfileID], ref)
			}
			if session.MediaFileID != 0 {
				mediaValues[strconv.Itoa(session.MediaFileID)] = append(mediaValues[strconv.Itoa(session.MediaFileID)], ref)
			}
		}
		if viewerEdge || !hasViewerEdge {
			rank := startedAtRank(session.StartedAtSource)
			if !session.StartedAt.IsZero() && rank > 0 {
				if rank > winningRank {
					winningRank = rank
					result.StartedAt = session.StartedAt
					result.StartedAtSource = session.StartedAtSource
					winningTimes = map[int64]struct{}{session.StartedAt.UnixNano(): {}}
				} else if rank == winningRank {
					winningTimes[session.StartedAt.UnixNano()] = struct{}{}
					if session.StartedAt.Before(result.StartedAt) {
						result.StartedAt = session.StartedAt
					}
				}
			}
			result.StartedAtDegraded = result.StartedAtDegraded || session.StartedAtDegraded
		}
		result.OpenObservations = saturatingAdd(result.OpenObservations, int64(session.OpenObservations))
		result.RequestCount = saturatingAdd(result.RequestCount, session.RequestCount)
		result.RealtimeConnectionAlive = result.RealtimeConnectionAlive || session.RealtimeConnectionAlive
		if session.LastByteAccepted.After(result.LastByteAccepted) {
			result.LastByteAccepted = session.LastByteAccepted
		}
		if session.LastObservationEnd.After(result.LastObservationEnd) {
			result.LastObservationEnd = session.LastObservationEnd
		}
		result.RoutesOverflowed = result.RoutesOverflowed || session.RoutesOverflowed
		result.BytesDegraded = result.BytesDegraded || session.RoutesOverflowed
		for _, route := range session.Routes {
			key := route.Method + "\x00" + route.Pattern + "\x00" + string(route.Role)
			merged, seen := routes[key]
			if !seen {
				merged = route
			} else {
				merged.Open = saturatingInt64ToInt(saturatingAdd(int64(merged.Open), int64(route.Open)))
				merged.Requests = saturatingAdd(merged.Requests, route.Requests)
				merged.BytesAccepted = saturatingAdd(merged.BytesAccepted, route.BytesAccepted)
				if route.LastByteAccepted.After(merged.LastByteAccepted) {
					merged.LastByteAccepted = route.LastByteAccepted
				}
				if route.LastObservationEnd.After(merged.LastObservationEnd) {
					merged.LastObservationEnd = route.LastObservationEnd
				}
			}
			routes[key] = merged
			if route.Role == RoleViewerEgress {
				result.ViewerBytesAccepted = saturatingAdd(result.ViewerBytesAccepted, route.BytesAccepted)
			}
			if route.Role == RoleInternalRelay {
				result.RelayBytesAccepted = saturatingAdd(result.RelayBytesAccepted, route.BytesAccepted)
			}
		}
		for _, value := range session.ViewerIPs {
			viewerIPs[value] = struct{}{}
		}
		for _, value := range session.DeviceIDs {
			deviceIDs[value] = struct{}{}
		}
		for _, value := range session.UserAgents {
			userAgents[value] = struct{}{}
		}
		for _, value := range session.ClientVariants {
			clients[value] = struct{}{}
		}
		for _, value := range session.MediaFileIDs {
			mediaFileIDs[value] = struct{}{}
		}
		if session.MediaFileID != 0 {
			mediaFileIDs[session.MediaFileID] = struct{}{}
		}
		for _, value := range session.PlayMethods {
			playMethods[value] = struct{}{}
		}
		if session.PlayMethod != "" {
			playMethods[session.PlayMethod] = struct{}{}
			result.PerPublisherPlayMethods = append(result.PerPublisherPlayMethods, PublisherValue{Publisher: ref, Value: session.PlayMethod})
		}
		for _, value := range session.TokenIssuedAts {
			tokenTimes[value.UnixNano()] = struct{}{}
		}
		for key, value := range session.TokenIssuedAtSources {
			result.TokenIssuedAtSources[key] = saturatingAdd(result.TokenIssuedAtSources[key], value)
		}
		for key, value := range session.Outcomes {
			result.Outcomes[key] = saturatingAdd(result.Outcomes[key], value)
		}
		result.ViewerIPsOverflowed = result.ViewerIPsOverflowed || session.ViewerIPsOverflowed
		result.DeviceIDsOverflowed = result.DeviceIDsOverflowed || session.DeviceIDsOverflowed
		result.ClientVariantsOverflowed = result.ClientVariantsOverflowed || session.ClientVariantsOverflowed
		result.UserAgentsOverflowed = result.UserAgentsOverflowed || session.UserAgentsOverflowed
		result.MediaFileIDsOverflowed = result.MediaFileIDsOverflowed || session.MediaFileIDsOverflowed
		result.PlayMethodsOverflowed = result.PlayMethodsOverflowed || session.PlayMethodsOverflowed
		result.TokenIssuedAtsOverflowed = result.TokenIssuedAtsOverflowed || session.TokenIssuedAtsOverflowed
		result.IdentityConflictsOverflowed = result.IdentityConflictsOverflowed || session.IdentityConflictsOverflowed
		for _, conflict := range session.IdentityConflicts {
			result.LocalIdentityConflicts = append(result.LocalIdentityConflicts, AttributedIdentityConflict{Publisher: ref, Conflict: conflict})
		}
	}
	if winningRank == 1 || len(winningTimes) > 1 {
		result.StartedAtDegraded = true
	}
	applyIdentity(&result, identityFieldSubject, subjectValues)
	applyIdentity(&result, identityFieldProfileID, profileValues)
	applyIdentity(&result, identityFieldMediaFileID, mediaValues)
	result.ViewerIPs, result.ViewerIPsOverflowed = cappedStrings(viewerIPs, params.MaxViewerIPsPerSession, result.ViewerIPsOverflowed)
	result.DeviceIDs, result.DeviceIDsOverflowed = cappedStrings(deviceIDs, params.MaxDeviceIDsPerSession, result.DeviceIDsOverflowed)
	result.UserAgents, result.UserAgentsOverflowed = cappedStrings(userAgents, params.MaxUserAgentsPerSession, result.UserAgentsOverflowed)
	result.PlayMethods, result.PlayMethodsOverflowed = cappedStrings(playMethods, params.MaxPlayMethodsPerSession, result.PlayMethodsOverflowed)
	result.MediaFileIDs, result.MediaFileIDsOverflowed = cappedInts(mediaFileIDs, params.MaxMediaFileIDsPerSession, result.MediaFileIDsOverflowed)
	result.ClientVariants, result.ClientVariantsOverflowed = cappedClients(clients, params.MaxClientVariantsPerSession, result.ClientVariantsOverflowed)
	result.TokenIssuedAts, result.TokenIssuedAtsOverflowed = cappedTimes(tokenTimes, params.MaxTokenIssuedAtPerSession, result.TokenIssuedAtsOverflowed)
	for _, route := range routes {
		result.Routes = append(result.Routes, route)
	}
	sort.Slice(result.Routes, func(i, j int) bool { return routeViewKey(result.Routes[i]) < routeViewKey(result.Routes[j]) })
	if params.MaxRoutesPerSession > 0 && len(result.Routes) > params.MaxRoutesPerSession {
		result.Routes = result.Routes[:params.MaxRoutesPerSession]
		result.RoutesOverflowed = true
		result.BytesDegraded = true
	}
	sort.Slice(result.IdentityConflicts, func(i, j int) bool { return result.IdentityConflicts[i].Field < result.IdentityConflicts[j].Field })
	if params.MaxIdentityConflictsPerSession > 0 && len(result.IdentityConflicts) > params.MaxIdentityConflictsPerSession {
		result.IdentityConflicts = result.IdentityConflicts[:params.MaxIdentityConflictsPerSession]
		result.IdentityConflictsOverflowed = true
	}
	sort.Slice(result.LocalIdentityConflicts, func(i, j int) bool {
		a, b := result.LocalIdentityConflicts[i], result.LocalIdentityConflicts[j]
		if a.Publisher.PublisherID != b.Publisher.PublisherID {
			return refLess(a.Publisher, b.Publisher)
		}
		if a.Conflict.Field != b.Conflict.Field {
			return a.Conflict.Field < b.Conflict.Field
		}
		if a.Conflict.Existing != b.Conflict.Existing {
			return a.Conflict.Existing < b.Conflict.Existing
		}
		if a.Conflict.Offered != b.Conflict.Offered {
			return a.Conflict.Offered < b.Conflict.Offered
		}
		return a.Conflict.ObservedAt.Before(b.Conflict.ObservedAt)
	})
	if params.MaxIdentityConflictsPerSession > 0 && len(result.LocalIdentityConflicts) > params.MaxIdentityConflictsPerSession {
		result.LocalIdentityConflicts = result.LocalIdentityConflicts[:params.MaxIdentityConflictsPerSession]
		result.IdentityConflictsOverflowed = true
	}
	sort.Slice(result.PerPublisherPlayMethods, func(i, j int) bool {
		if result.PerPublisherPlayMethods[i].Publisher.PublisherID == result.PerPublisherPlayMethods[j].Publisher.PublisherID {
			return result.PerPublisherPlayMethods[i].Value < result.PerPublisherPlayMethods[j].Value
		}
		return refLess(result.PerPublisherPlayMethods[i].Publisher, result.PerPublisherPlayMethods[j].Publisher)
	})
	return result
}

func applyIdentity(result *GlobalSessionView, field string, values map[string][]PublisherRef) {
	if len(values) == 0 {
		return
	}
	if len(values) == 1 {
		for value := range values {
			switch field {
			case identityFieldSubject:
				parts := strings.SplitN(value, "\x00", 2)
				result.Subject = Subject{Kind: SubjectKind(parts[0]), ID: parts[1]}
			case identityFieldProfileID:
				result.ProfileID = value
			case identityFieldMediaFileID:
				result.MediaFileID, _ = strconv.Atoi(value)
			}
		}
		return
	}
	conflict := GlobalIdentityConflict{Field: field}
	for value, publishers := range values {
		sort.Slice(publishers, func(i, j int) bool { return refLess(publishers[i], publishers[j]) })
		conflict.Values = append(conflict.Values, AttributedValue{Value: strings.Replace(value, "\x00", ":", 1), Publishers: publishers})
	}
	sort.Slice(conflict.Values, func(i, j int) bool { return conflict.Values[i].Value < conflict.Values[j].Value })
	result.IdentityConflicts = append(result.IdentityConflicts, conflict)
	result.HasIdentityConflict = true
}

func cappedStrings(values map[string]struct{}, maximum int, overflow bool) ([]string, bool) {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	if maximum > 0 && len(out) > maximum {
		out = out[:maximum]
		overflow = true
	}
	return out, overflow
}
func cappedInts(values map[int]struct{}, maximum int, overflow bool) ([]int, bool) {
	out := make([]int, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Ints(out)
	if maximum > 0 && len(out) > maximum {
		out = out[:maximum]
		overflow = true
	}
	return out, overflow
}
func cappedClients(values map[ClientVariant]struct{}, maximum int, overflow bool) ([]ClientVariant, bool) {
	out := make([]ClientVariant, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Slice(out, func(i, j int) bool { return clientVariantKey(out[i]) < clientVariantKey(out[j]) })
	if maximum > 0 && len(out) > maximum {
		out = out[:maximum]
		overflow = true
	}
	return out, overflow
}
func cappedTimes(values map[int64]struct{}, maximum int, overflow bool) ([]time.Time, bool) {
	nanos := make([]int64, 0, len(values))
	for value := range values {
		nanos = append(nanos, value)
	}
	sort.Slice(nanos, func(i, j int) bool { return nanos[i] < nanos[j] })
	if maximum > 0 && len(nanos) > maximum {
		nanos = nanos[:maximum]
		overflow = true
	}
	out := make([]time.Time, len(nanos))
	for i, value := range nanos {
		out[i] = time.Unix(0, value)
	}
	return out, overflow
}

func globalEpoch(contributions []publisherContribution) string {
	hash := sha256.New()
	for _, contribution := range contributions {
		_, _ = fmt.Fprintf(hash, "%s\x00%d\x00%d\n", contribution.ref.PublisherID, contribution.snapshot.PublisherEpoch, contribution.snapshot.Sequence)
	}
	return hex.EncodeToString(hash.Sum(nil)[:16])
}

func saturatingAdd(a, b int64) int64 {
	if b > 0 && a > math.MaxInt64-b {
		return math.MaxInt64
	}
	if b < 0 && a < math.MinInt64-b {
		return math.MinInt64
	}
	return a + b
}

func saturatingInt(a, b int) int {
	if b > 0 && a > math.MaxInt-b {
		return math.MaxInt
	}
	return a + b
}
func saturatingInt64ToInt(value int64) int {
	if value > int64(math.MaxInt) {
		return math.MaxInt
	}
	return int(value)
}
func refLess(a, b PublisherRef) bool {
	if a.PublisherID == b.PublisherID {
		return a.NodeID < b.NodeID
	}
	return a.PublisherID < b.PublisherID
}
func routeViewKey(route RouteActivityView) string {
	return route.Method + "\x00" + route.Pattern + "\x00" + string(route.Role)
}
func cloneTransfer(value TransferView) TransferView {
	value.Outcomes = cloneOutcomes(value.Outcomes)
	return value
}
