package requests

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"
)

// staleDownloadProgress is how long a target's download progress outlives
// its last report once its server stops answering for it. A pass that asks
// about a target and gets no status back (the plugin skipped a server that
// errored or no longer has the title, or the call failed) clears progress
// older than this, so a frozen figure stops showing and clients stop polling
// for it. Until then the progress stays, since one missed answer is usually
// a blip.
const staleDownloadProgress = 15 * time.Minute

// RefreshDownloads is the download refresh pass. For each active request with
// a downloading target that has download progress, least recently asked about
// first, it asks the plugins that declare reports_download_progress about
// those targets and applies the answer the way reconcile does: status
// transitions and download progress. It submits nothing, checks no library
// presence and sends no notification; the reconcile pass keeps those.
//
// Only a target with progress is polled. The reconcile pass records a
// download's first progress, and a target leaves this pass as soon as its
// progress clears, so a target whose plugin reports none (a legacy plugin, or
// one with nothing in its download queue for it) costs no call a minute.
// Queued targets stay on the reconcile cadence, since a title can sit queued
// for months before release.
//
// budget bounds the pass, zero or less leaving it to ctx: once it has run
// out, the call in flight is cut and no further request is asked about. The
// requests left over are the least recently asked about on the next pass, and
// the pass reports no error for them.
func (s *Service) RefreshDownloads(ctx context.Context, limit int, budget time.Duration) (DownloadRefreshResult, error) {
	if s == nil || s.store == nil {
		return DownloadRefreshResult{}, fmt.Errorf("request service is not configured")
	}
	if s.router == nil {
		return DownloadRefreshResult{}, nil
	}
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	checkCtx := ctx
	if budget > 0 {
		var cancel context.CancelFunc
		checkCtx, cancel = context.WithTimeout(ctx, budget)
		defer cancel()
	}
	reqs, err := s.store.ListDownloadingRequests(ctx, limit)
	if err != nil || len(reqs) == 0 {
		return DownloadRefreshResult{}, err
	}
	fc, err := s.newFulfillContext(ctx)
	if err != nil {
		return DownloadRefreshResult{}, err
	}
	ids := make([]string, 0, len(reqs))
	for _, req := range reqs {
		ids = append(ids, req.ID)
	}
	byRequest, err := s.store.ListTargetsForRequests(ctx, ids)
	if err != nil {
		return DownloadRefreshResult{}, err
	}
	var result DownloadRefreshResult
	for i, req := range reqs {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if checkCtx.Err() != nil {
			slog.InfoContext(ctx, "request download refresh ran out of time; the rest wait for the next pass", "component", "requests",
				"budget", budget,
				"requests_left", len(reqs)-i,
			)
			break
		}
		change, checked, err := s.refreshRequestDownloads(ctx, checkCtx, fc, *req, byRequest[req.ID])
		if checked {
			result.Checked++
		}
		if change != reconcileUnchanged {
			result.Updated++
		}
		if err != nil {
			slog.WarnContext(ctx, "request download refresh failed", "component", "requests",
				"request_id", req.ID,
				"media_type", req.MediaType,
				"tmdb_id", req.TMDBID,
				"err", err,
			)
			result.Errors++
		}
	}
	return result, nil
}

// refreshRequestDownloads checks one request's downloading targets that have
// progress and whose plugin reports it, and applies what it answers. A target
// with progress that its server can no longer be asked about (gone, disabled
// or unusable), or whose plugin no longer declares progress, has the progress
// cleared: it is stale, and clearing it takes the target off this pass.
// checkCtx bounds only the plugin calls; the writes that apply their answers
// run on ctx, so a call the pass's budget cuts still moves its targets back in
// the rotation. A target whose plugin's features cannot be read is settled as
// unanswered, so it too moves back instead of heading every batch, and the
// request's other targets are still asked about. checked is false when no
// target was asked about.
func (s *Service) refreshRequestDownloads(ctx, checkCtx context.Context, fc *fulfillContext, req Request, targets []Target) (change reconcileChange, checked bool, err error) {
	var polled []Target
	var featureErr error
	for _, t := range targets {
		if t.Status != StatusDownloading || t.Download == nil {
			continue
		}
		reports, err := s.targetReportsProgress(ctx, fc, req.MediaType, t)
		if err != nil {
			featureErr = errors.Join(featureErr, err)
			if err := s.settleUnansweredDownload(ctx, t); err != nil {
				return reconcileUnchanged, false, errors.Join(featureErr, err)
			}
			continue
		}
		if !reports {
			if err := s.store.UpdateTargetDownload(ctx, t.ID, nil); err != nil {
				return reconcileUnchanged, false, err
			}
			continue
		}
		polled = append(polled, t)
	}
	if len(polled) == 0 {
		return reconcileUnchanged, false, featureErr
	}
	statuses, checkErr := s.checkTargetStatuses(checkCtx, req, polled, fc)
	change, err = s.applyTargetStatuses(ctx, polled, statuses)
	if err != nil {
		return reconcileUnchanged, true, errors.Join(featureErr, err)
	}
	return change, true, errors.Join(featureErr, checkErr)
}

// HasDownloadsToRefresh reports whether the download refresh pass has any
// work: an active request with a downloading target that has progress.
func (s *Service) HasDownloadsToRefresh(ctx context.Context) (bool, error) {
	if s == nil || s.store == nil || s.router == nil {
		return false, nil
	}
	reqs, err := s.store.ListDownloadingRequests(ctx, 1)
	return len(reqs) > 0, err
}

// settleUnansweredDownload handles a target that a pass asked about, or meant
// to, without getting its status back. A queued or downloading target with
// progress keeps it, marked as asked about so the download refresh pass takes
// the others first next time, until the progress is staleDownloadProgress
// old; then the progress is cleared. Any other target is not written.
func (s *Service) settleUnansweredDownload(ctx context.Context, t Target) error {
	if t.Download == nil || (t.Status != StatusQueued && t.Status != StatusDownloading) {
		return nil
	}
	if s.now().Sub(t.Download.UpdatedAt) > staleDownloadProgress {
		return s.store.UpdateTargetDownload(ctx, t.ID, nil)
	}
	return s.store.MarkTargetDownloadChecked(ctx, t.ID)
}

// targetReportsProgress reports whether the router capability that
// checkTargetStatuses would ask about a target declares
// reports_download_progress. A target it would skip reports none.
func (s *Service) targetReportsProgress(ctx context.Context, fc *fulfillContext, mediaType MediaType, t Target) (bool, error) {
	var installationID int
	var capabilityID string
	if t.IntegrationID == "" {
		conns, id, capability, err := s.resolveRouterConnections(ctx, fc, mediaType)
		if err != nil || len(conns) == 0 {
			return false, err
		}
		installationID, capabilityID = id, capability
	} else {
		in := integrationByID(fc, t.IntegrationID)
		if !statusCheckable(in) {
			return false, nil
		}
		installationID, capabilityID = *in.InstallationID, in.CapabilityID
	}
	features, err := s.routerFeatures(ctx, fc, installationID, capabilityID)
	return features.ReportsDownloadProgress, err
}

// keepsReportedProgress reports whether the progress in a router's statuses
// is recorded: only a plugin that declares reports_download_progress has its
// progress kept. The download refresh pass clears the progress of any other,
// so recording it would make it come and go between passes. Statuses without
// progress read no features, and when the declaration cannot be read the
// report stands.
func (s *Service) keepsReportedProgress(ctx context.Context, fc *fulfillContext, installationID int, capabilityID string, statuses []RouterTargetStatus) (bool, error) {
	if !slices.ContainsFunc(statuses, func(st RouterTargetStatus) bool { return st.Progress != nil }) {
		return true, nil
	}
	features, err := s.routerFeatures(ctx, fc, installationID, capabilityID)
	if err != nil {
		return true, err
	}
	return features.ReportsDownloadProgress, nil
}

// activeRequestDownload loads an active request's targets for its download
// progress. Only a queued or downloading request can have any, so any other
// costs no query.
func (s *Service) activeRequestDownload(ctx context.Context, req *Request) (*DownloadProgress, error) {
	if req == nil || req.Outcome != OutcomeActive || (req.Status != StatusQueued && req.Status != StatusDownloading) {
		return nil, nil
	}
	targets, err := s.store.ListTargets(ctx, req.ID)
	if err != nil {
		return nil, err
	}
	return (&Request{Targets: targets}).Download(), nil
}
