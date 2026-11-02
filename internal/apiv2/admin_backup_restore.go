package apiv2

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/adminjob"
	"github.com/Silo-Server/silo-server/internal/backup"
	"github.com/Silo-Server/silo-server/internal/models"
)

type AdminBackupRestoreSetting struct {
	Key             string `json:"key"`
	Class           string `json:"class" enum:"portable,external_service" doc:"external_service: connects to an outside service (mail, requests, metadata providers)"`
	Status          string `json:"status" enum:"on_this_server,differs,needs_another_key" doc:"on_this_server: same value here; needs_another_key: encrypted with another SECRET_KEY and never written"`
	Masked          bool   `json:"masked" doc:"An encrypted or sensitive setting: both values are omitted"`
	BackupValue     string `json:"backup_value" doc:"The value in the backup; empty when masked"`
	CurrentValue    string `json:"current_value" doc:"The value on this server; empty when masked"`
	RestartRequired bool   `json:"restart_required" doc:"The setting applies after a restart"`
}

type AdminBackupRestorePlugin struct {
	Version          string   `json:"version"`
	InstalledVersion string   `json:"installed_version,omitempty" doc:"The version installed here, when the plugin is installed"`
	Enabled          bool     `json:"enabled" doc:"Whether the plugin was enabled in the backup"`
	ConfigKeys       []string `json:"config_keys"`
	ConfigRestorable bool     `json:"config_restorable" doc:"False when the configuration was encrypted with another SECRET_KEY: the plugin installs without it and stays disabled"`
}

type AdminBackupRestoreLibrary struct {
	Type         string   `json:"type"`
	Paths        []string `json:"paths"`
	LocalID      *ID      `json:"local_id,omitempty" doc:"The library here with the identical folder set"`
	MissingPaths []string `json:"missing_paths,omitempty" doc:"Folders this server cannot open"`
}

type AdminBackupRestoreCollection struct {
	LibraryIDs   []ID     `json:"library_ids,omitempty" doc:"Libraries here the collection maps to"`
	Items        int      `json:"items"`
	MissingItems int      `json:"missing_items" doc:"Items the collection here lacks that a restore adds"`
	UnknownItems int      `json:"unknown_items" doc:"Items not in this server's catalog, left out"`
	LocalID      string   `json:"local_id,omitempty" doc:"The collection here: for a manual collection the one with the same name in the mapped library, for a template collection the one the same template created for the mapped library"`
	TemplateID   string   `json:"template_id,omitempty" doc:"A template collection's template in the collection template catalog"`
	BundleID     string   `json:"bundle_id,omitempty" doc:"The template bundle that created a template collection"`
	Changes      []string `json:"changes,omitempty" doc:"A template collection's fields the admin changed from the template's defaults that a restore writes here: title, description, visibility, featured, sort order, item sort, source, sync schedule, poster, backdrop"`
}

// AdminBackupRestoreHomeSection is a backup page section against this
// server.
type AdminBackupRestoreHomeSection struct {
	Scope       string   `json:"scope" enum:"home,library"`
	LibraryID   *ID      `json:"library_id,omitempty" doc:"The library here whose page holds the section"`
	SectionType string   `json:"section_type"`
	LocalID     string   `json:"local_id,omitempty" doc:"The matching section here: the same id on the server that took the backup, else the one section of the page with the same type and title"`
	Changes     []string `json:"changes,omitempty" doc:"What a restore replaces in the section here: title, position, featured, item limit, enabled, configuration, and N collection items"`
}

type AdminBackupRestoreItem struct {
	ID              string                         `json:"id" doc:"The id to choose this item by in a restore request"`
	Kind            string                         `json:"kind" enum:"settings_page,plugin,library,collection,template_collection,item_artwork,branding,home_section"`
	Label           string                         `json:"label"`
	Status          string                         `json:"status" enum:"on_this_server,not_on_this_server,differs,folder_missing,ambiguous,needs_another_key"`
	Selectable      bool                           `json:"selectable" doc:"Whether the item can be chosen. Items on this server already have nothing to restore."`
	Reason          string                         `json:"reason,omitempty" enum:"same,path_in_use,folder_missing,library_not_restorable,library_not_mapped,ambiguous_name,needs_another_key,config_needs_another_key,other_version,item_not_on_this_server,file_missing,not_in_backup,changed_since_preview,template_not_available,collection_not_mapped"`
	Message         string                         `json:"message,omitempty" doc:"What restoring the item does or why it cannot be chosen, for display"`
	Requires        []string                       `json:"requires" doc:"Items (section:id) that must be chosen with this one: a collection whose library is restored by the same request"`
	RestartRequired bool                           `json:"restart_required"`
	ExternalService bool                           `json:"external_service" doc:"A settings page whose differing settings connect to an outside service"`
	MissingBlobs    []string                       `json:"missing_blobs" doc:"Files the item references that were missing when the backup was taken"`
	Settings        []AdminBackupRestoreSetting    `json:"settings,omitempty"`
	Plugin          *AdminBackupRestorePlugin      `json:"plugin,omitempty"`
	Library         *AdminBackupRestoreLibrary     `json:"library,omitempty"`
	Collection      *AdminBackupRestoreCollection  `json:"collection,omitempty"`
	HomeSection     *AdminBackupRestoreHomeSection `json:"home_section,omitempty"`
}

type AdminBackupRestoreSection struct {
	ID    string                   `json:"id" enum:"libraries,plugins,settings,collections,home_sections"`
	Count int                      `json:"count" doc:"Items the section holds in the backup"`
	Items []AdminBackupRestoreItem `json:"items"`
}

type AdminBackupLibraryMapping struct {
	BackupID   ID     `json:"backup_id"`
	Name       string `json:"name"`
	LocalID    *ID    `json:"local_id,omitempty" doc:"The library here with the identical folder set; absent when the library is not on this server"`
	Status     string `json:"status" enum:"on_this_server,not_on_this_server,folder_missing"`
	Selectable bool   `json:"selectable"`
}

type AdminBackupRestoreSet struct {
	BackupID      string   `json:"backup_id,omitempty" doc:"The backup previewed"`
	Digest        string   `json:"digest" doc:"SHA-256 of the restore set; a restore request must carry it"`
	FormatVersion int      `json:"format_version"`
	ServerID      string   `json:"server_id" doc:"The server the backup was taken on"`
	ServerName    string   `json:"server_name"`
	SiloVersion   string   `json:"silo_version"`
	CreatedAt     Instant  `json:"created_at"`
	SameServer    bool     `json:"same_server"`
	KeyMatches    bool     `json:"key_matches" doc:"Whether the backup was taken with this server's SECRET_KEY; when false, encrypted settings and plugin configuration cannot be restored"`
	MissingBlobs  []string `json:"missing_blobs"`
	// NeverRestoredSettings lets a client name what a restore leaves alone
	// without keeping its own list.
	NeverRestoredSettings []string                    `json:"never_restored_settings" doc:"Setting keys a restore never writes because they describe this server (its identity, storage, infrastructure, and credentials). An entry ending in . or _ is a key prefix."`
	LibraryMap            []AdminBackupLibraryMapping `json:"library_map"`
	Sections              []AdminBackupRestoreSection `json:"sections"`
}

type AdminBackupRestoreSetOutput struct{ Body AdminBackupRestoreSet }

type AdminBackupRestoreSelection struct {
	Libraries   []string `json:"libraries,omitempty" maxItems:"20000" doc:"Backup library ids"`
	Plugins     []string `json:"plugins,omitempty" maxItems:"20000" doc:"Plugin ids"`
	Settings    []string `json:"settings,omitempty" maxItems:"20000" doc:"Settings page ids"`
	Collections []string `json:"collections,omitempty" maxItems:"20000" doc:"Collection, template collection, item artwork, and branding item ids"`
	// HomeSections are page section ids.
	HomeSections []string `json:"home_sections,omitempty" maxItems:"20000" doc:"Page section item ids (section:<id>)"`
}

type AdminBackupRestoreBody struct {
	Digest    string                      `json:"digest" pattern:"^[0-9a-f]{64}$" doc:"The digest the preview returned"`
	Selection AdminBackupRestoreSelection `json:"selection"`
}

type AdminBackupRestoreInput struct {
	ID   string `path:"id" minLength:"1" maxLength:"64" pattern:"^[0-9]{8}T[0-9]{6}(\\.[0-9]{3})?Z-[a-z-]+$"`
	Body AdminBackupRestoreBody
}

type AdminBackupRestoreOutput struct {
	Location   string `header:"Location"`
	RetryAfter string `header:"Retry-After"`
	Body       AdminTaskJob
}

// AdminBackupRestoreReportItem is one item's outcome in a restore report.
type AdminBackupRestoreReportItem struct {
	Section         string   `json:"section" enum:"libraries,plugins,settings,collections,home_sections"`
	ID              string   `json:"id"`
	Kind            string   `json:"kind" enum:"settings_page,plugin,library,collection,template_collection,item_artwork,branding,home_section"`
	Label           string   `json:"label"`
	Outcome         string   `json:"outcome" enum:"restored,unchanged,left_out,failed"`
	Reason          string   `json:"reason,omitempty" enum:"same,path_in_use,folder_missing,library_not_restorable,library_not_mapped,ambiguous_name,needs_another_key,config_needs_another_key,other_version,item_not_on_this_server,file_missing,not_in_backup,changed_since_preview,template_not_available,collection_not_mapped" doc:"Why a left_out item was left out"`
	Message         string   `json:"message,omitempty"`
	Error           string   `json:"error,omitempty" doc:"Why a failed item failed"`
	RestartRequired bool     `json:"restart_required"`
	RestartKeys     []string `json:"restart_keys"`
	Notes           []string `json:"notes"`
	MissingBlobs    []string `json:"missing_blobs"`
}

type AdminBackupRestoreCounts struct {
	Restored  int `json:"restored"`
	Unchanged int `json:"unchanged"`
	LeftOut   int `json:"left_out"`
	Failed    int `json:"failed"`
}

// AdminBackupRestoreReport is a restore job's report: what committed, item
// by item, even when the job was canceled or interrupted.
type AdminBackupRestoreReport struct {
	BackupID              string                         `json:"backup_id,omitempty" doc:"The backup restored from"`
	BeforeRestoreBackupID string                         `json:"before_restore_backup_id,omitempty" doc:"The recovery point taken before anything was restored. Restoring it in full with silo backup restore goes back to how the server was."`
	LibraryMap            []AdminBackupLibraryMapping    `json:"library_map"`
	Items                 []AdminBackupRestoreReportItem `json:"items"`
	Counts                AdminBackupRestoreCounts       `json:"counts"`
	Interrupted           bool                           `json:"interrupted" doc:"The server running the restore stopped; items listed committed, the rest did not. Running the same restore again is safe."`
	Error                 string                         `json:"error,omitempty" doc:"Why the restore stopped, when it did"`
	ErrorCode             string                         `json:"error_code,omitempty" enum:"restore_set_changed,busy,before_restore_backup_failed,interrupted,failed" doc:"Classifies error: restore_set_changed (the backup changed since its preview; nothing was restored), busy (another backup or restore held the lock), before_restore_backup_failed (nothing was restored), interrupted, or failed"`
}

// AdminBackupRestoreSource names the backup a restore job restores from.
// It is present from the moment the job is queued.
type AdminBackupRestoreSource struct {
	BackupID string `json:"backup_id,omitempty" doc:"The backup restored from"`
}

func registerAdminBackupRestore(reg *Registry, op func(method, path, id, summary string) Operation) {
	preview := op(http.MethodGet, "/{id}/restore-set", "getAdminBackupRestoreSet",
		"Read a backup's restore set and compare each section with this server: per-item status, the library mapping, per-setting differences with encrypted values masked, and the digest a restore request must carry. 409 when the backup has no restore set or is from a newer Silo.")
	preview.Errors = []int{http.StatusConflict}
	Register(reg, preview, func(ctx context.Context, in *AdminBackupIDInput) (*AdminBackupRestoreSetOutput, error) {
		svc, p := reg.backups()
		if p != nil {
			return nil, p
		}
		out, err := svc.Preview(ctx, in.ID)
		if err != nil {
			return nil, backupProblem(err)
		}
		return &AdminBackupRestoreSetOutput{Body: adminBackupRestoreSetOf(out)}, nil
	})

	restore := op(http.MethodPost, "/{id}/restore", "startAdminBackupRestore",
		"Restore the chosen items of a backup as a backup_restore job. A backup of this server is taken first; follow the job through the admin jobs endpoints. 409 when another backup or restore is running, or the backup changed since the preview.")
	restore.DefaultStatus = http.StatusAccepted
	restore.RetrySafety = RetrySafetyNonRetryable
	restore.Errors = []int{http.StatusConflict}
	Register(reg, restore, func(ctx context.Context, in *AdminBackupRestoreInput) (*AdminBackupRestoreOutput, error) {
		svc, p := reg.backups()
		if p != nil {
			return nil, p
		}
		req := restoreRequestOf(in.Body)
		req.BackupID = in.ID
		job, err := svc.StartRestore(ctx, claimsFrom(ctx).UserID, req)
		if err != nil {
			return nil, restoreProblem(err)
		}
		return &AdminBackupRestoreOutput{Location: Prefix + "/admin/jobs/" + job.ID, RetryAfter: "5", Body: reg.adminTaskJobOf(ctx, job, true)}, nil
	})
}

// restoreRequestOf reads a restore request's digest and selection; the
// caller names the source.
func restoreRequestOf(body AdminBackupRestoreBody) backup.RestoreRequest {
	sel := body.Selection
	req := backup.RestoreRequest{Digest: body.Digest, Selection: map[string][]string{}}
	for section, ids := range map[string][]string{
		backup.SectionLibraries: sel.Libraries, backup.SectionPlugins: sel.Plugins,
		backup.SectionSettings: sel.Settings, backup.SectionCollections: sel.Collections,
		backup.SectionHomeSections: sel.HomeSections,
	} {
		if len(ids) > 0 {
			req.Selection[section] = ids
		}
	}
	return req
}

// restoreProblem maps a refused restore request: one already running
// points at its job.
func restoreProblem(err error) error {
	if conflict, ok := errors.AsType[*adminjob.ActiveJobConflictError](err); ok {
		p := NewProblem(TypeConflict, "A restore is already queued or running.")
		if conflict.Job != nil {
			p = p.WithHeader("Location", Prefix+"/admin/jobs/"+conflict.Job.ID)
		}
		return p
	}
	if selection, ok := errors.AsType[*backup.SelectionError](err); ok {
		return NewProblem(TypeValidationFailed, selection.Message)
	}
	return backupProblem(err)
}

func adminBackupRestoreSetOf(p *backup.RestorePreview) AdminBackupRestoreSet {
	out := AdminBackupRestoreSet{
		BackupID: p.BackupID, Digest: p.Digest, FormatVersion: p.FormatVersion, ServerID: p.ServerID, ServerName: p.ServerName,
		SiloVersion: p.SiloVersion, CreatedAt: NewInstant(p.CreatedAt), SameServer: p.SameServer, KeyMatches: p.KeyMatches,
		MissingBlobs: nonNilStrings(p.MissingBlobs), NeverRestoredSettings: nonNilStrings(p.NeverRestoredSettings),
		LibraryMap: []AdminBackupLibraryMapping{}, Sections: []AdminBackupRestoreSection{},
	}
	for _, l := range p.Libraries {
		out.LibraryMap = append(out.LibraryMap, AdminBackupLibraryMapping{BackupID: IDFromInt(int64(l.BackupID)), Name: l.Name, LocalID: idOfIntPtr(l.LocalID), Status: l.Status, Selectable: l.Selectable})
	}
	for _, s := range p.Sections {
		section := AdminBackupRestoreSection{ID: s.ID, Count: s.Count, Items: []AdminBackupRestoreItem{}}
		for _, it := range s.Items {
			item := AdminBackupRestoreItem{ID: it.ID, Kind: it.Kind, Label: it.Label, Status: it.Status, Selectable: it.Selectable,
				Reason: it.Reason, Message: it.Message, Requires: nonNilStrings(it.Requires), RestartRequired: it.RestartRequired,
				ExternalService: it.ExternalService, MissingBlobs: nonNilStrings(it.MissingBlobs)}
			for _, st := range it.Settings {
				item.Settings = append(item.Settings, AdminBackupRestoreSetting{Key: st.Key, Class: string(st.Class), Status: st.Status, Masked: st.Masked,
					BackupValue: st.BackupValue, CurrentValue: st.CurrentValue, RestartRequired: st.RestartRequired})
			}
			if pl := it.Plugin; pl != nil {
				item.Plugin = &AdminBackupRestorePlugin{Version: pl.Version, InstalledVersion: pl.InstalledVersion, Enabled: pl.Enabled,
					ConfigKeys: nonNilStrings(pl.ConfigKeys), ConfigRestorable: pl.ConfigRestorable}
			}
			if lib := it.Library; lib != nil {
				item.Library = &AdminBackupRestoreLibrary{Type: lib.Type, Paths: nonNilStrings(lib.Paths), LocalID: idOfIntPtr(lib.LocalID), MissingPaths: lib.MissingPaths}
			}
			if c := it.Collection; c != nil {
				item.Collection = &AdminBackupRestoreCollection{Items: c.Items, MissingItems: c.MissingItems, UnknownItems: c.UnknownItems, LocalID: c.LocalID,
					TemplateID: c.TemplateID, BundleID: c.BundleID, Changes: c.Changes}
				for _, id := range c.LibraryIDs {
					item.Collection.LibraryIDs = append(item.Collection.LibraryIDs, IDFromInt(int64(id)))
				}
			}
			if h := it.HomeSection; h != nil {
				item.HomeSection = &AdminBackupRestoreHomeSection{Scope: h.Scope, LibraryID: idOfIntPtr(h.LibraryID), SectionType: h.SectionType, LocalID: h.LocalID, Changes: h.Changes}
			}
			section.Items = append(section.Items, item)
		}
		out.Sections = append(out.Sections, section)
	}
	return out
}

// backupRestoreSourceOf reads a restore job's source from its request, so
// a queued job names its backup before anything runs.
func backupRestoreSourceOf(job *models.AdminJob) *AdminBackupRestoreSource {
	var req backup.RestoreRequest
	if json.Unmarshal(job.RequestPayload, &req) != nil || req.BackupID == "" {
		return nil
	}
	return &AdminBackupRestoreSource{BackupID: req.BackupID}
}

// backupRestoreReportOf projects a restore job's report. A job stale
// recovery marked failed reports interrupted.
func backupRestoreReportOf(job *models.AdminJob) *AdminBackupRestoreReport {
	var report backup.SelectiveReport
	if len(job.ResultPayload) == 0 || json.Unmarshal(job.ResultPayload, &report) != nil {
		return nil
	}
	out := &AdminBackupRestoreReport{BackupID: report.BackupID, BeforeRestoreBackupID: report.BeforeRestoreBackupID,
		LibraryMap: []AdminBackupLibraryMapping{}, Items: []AdminBackupRestoreReportItem{}, Error: report.Error, ErrorCode: report.ErrorCode}
	if out.BackupID == "" {
		var req backup.RestoreRequest
		_ = json.Unmarshal(job.RequestPayload, &req)
		out.BackupID = req.BackupID
	}
	if job.Status == adminjob.StatusFailed && job.ErrorMessage == adminjob.BackupRestoreInterrupted {
		out.Interrupted, out.ErrorCode = true, "interrupted"
		if out.Error == "" {
			out.Error = "The server running the restore stopped before it finished."
		}
	}
	backupIDs := make([]string, 0, len(report.LibraryMap))
	for id := range report.LibraryMap {
		backupIDs = append(backupIDs, id)
	}
	sort.Slice(backupIDs, func(i, j int) bool {
		a, _ := strconv.Atoi(backupIDs[i])
		b, _ := strconv.Atoi(backupIDs[j])
		return a < b
	})
	for _, id := range backupIDs {
		local := IDFromInt(int64(report.LibraryMap[id]))
		out.LibraryMap = append(out.LibraryMap, AdminBackupLibraryMapping{BackupID: ID(id), LocalID: &local, Status: backup.ItemOnThisServer})
	}
	for _, it := range report.Items {
		out.Items = append(out.Items, AdminBackupRestoreReportItem{Section: it.Section, ID: it.ID, Kind: it.Kind, Label: it.Label, Outcome: it.Outcome,
			Reason: it.Reason, Message: it.Message, Error: it.Error, RestartRequired: it.RestartRequired, RestartKeys: nonNilStrings(it.RestartKeys),
			Notes: nonNilStrings(it.Notes), MissingBlobs: nonNilStrings(it.MissingBlobs)})
	}
	out.Counts = AdminBackupRestoreCounts{Restored: report.Counts[backup.OutcomeRestored], Unchanged: report.Counts[backup.OutcomeUnchanged],
		LeftOut: report.Counts[backup.OutcomeLeftOut], Failed: report.Counts[backup.OutcomeFailed]}
	return out
}
