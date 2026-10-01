package apiv2

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/jellycompat"
)

type AdminJellyfinCompatStatusService interface {
	ReadAdminJellyfinCompatStatus(context.Context) (jellycompat.WebComponentStatus, error)
}
type AdminJellyfinWebOperation struct {
	ID              string   `json:"id"`
	Kind            string   `json:"kind" enum:"install,remove"`
	State           string   `json:"state" enum:"running,succeeded,failed"`
	PID             int      `json:"pid,omitempty"`
	Process         string   `json:"process,omitempty"`
	Host            string   `json:"host,omitempty"`
	StartedAt       *Instant `json:"started_at,omitempty"`
	CompletedAt     *Instant `json:"completed_at,omitempty"`
	Phase           string   `json:"phase,omitempty" enum:"preparing,downloading,installing_dependencies,building,staging,activating,persisting_settings,removing"`
	ProgressPercent int      `json:"progress_percent,omitempty"`
	Message         string   `json:"message,omitempty"`
	Error           string   `json:"error,omitempty"`
}
type AdminJellyfinCompatStatus struct {
	Enabled           bool                                   `json:"enabled"`
	APIState          string                                 `json:"api_state" enum:"disabled,enabled,error"`
	Listen            string                                 `json:"listen"`
	PublicURL         string                                 `json:"public_url"`
	EmulatedVersion   string                                 `json:"emulated_server_version"`
	ServerName        string                                 `json:"server_name"`
	WebEnabled        bool                                   `json:"web_enabled"`
	WebState          string                                 `json:"web_state" enum:"missing,installing,removing,installed,failed,update_available"`
	PinnedVersion     string                                 `json:"pinned_version"`
	InstalledVersion  string                                 `json:"installed_version,omitempty"`
	SourceURL         string                                 `json:"source_url"`
	Tag               string                                 `json:"tag,omitempty"`
	CommitSHA         string                                 `json:"commit_sha,omitempty"`
	Checksum          string                                 `json:"checksum,omitempty"`
	InstallRoot       string                                 `json:"install_root"`
	InstallPath       string                                 `json:"install_path"`
	InstalledAt       *Instant                               `json:"installed_at,omitempty"`
	LicensePresent    bool                                   `json:"license_present"`
	ProvenancePresent bool                                   `json:"provenance_present"`
	InstallerReady    bool                                   `json:"installer_ready"`
	Prerequisites     []jellycompat.WebInstallerPrerequisite `json:"prerequisites"`
	Operation         *AdminJellyfinWebOperation             `json:"operation,omitempty"`
	LastError         string                                 `json:"last_error,omitempty"`
	RestartRequired   bool                                   `json:"restart_required"`
}
type AdminJellyfinCompatStatusOutput struct{ Body AdminJellyfinCompatStatus }

func adminJellyfinCompatStatusOf(s jellycompat.WebComponentStatus) AdminJellyfinCompatStatus {
	out := AdminJellyfinCompatStatus{
		Enabled:           s.Enabled,
		APIState:          s.APIState,
		Listen:            s.Listen,
		PublicURL:         s.PublicURL,
		EmulatedVersion:   s.EmulatedVersion,
		ServerName:        s.ServerName,
		WebEnabled:        s.WebEnabled,
		WebState:          string(s.WebState),
		PinnedVersion:     s.PinnedVersion,
		InstalledVersion:  s.InstalledVersion,
		SourceURL:         s.SourceURL,
		Tag:               s.Tag,
		CommitSHA:         s.CommitSHA,
		Checksum:          s.Checksum,
		InstallRoot:       s.InstallRoot,
		InstallPath:       s.InstallPath,
		InstalledAt:       instantOfRFC3339(&s.InstalledAt),
		LicensePresent:    s.LicensePresent,
		ProvenancePresent: s.ProvenancePresent,
		InstallerReady:    s.InstallerReady,
		Prerequisites:     s.Prerequisites,
		LastError:         s.LastError,
		RestartRequired:   s.RestartRequired,
	}
	if s.Operation != nil {
		o := s.Operation
		out.Operation = &AdminJellyfinWebOperation{
			ID:              o.ID,
			Kind:            string(o.Kind),
			State:           string(o.State),
			PID:             o.PID,
			Process:         o.Process,
			Host:            o.Host,
			StartedAt:       instantOfRFC3339(&o.StartedAt),
			CompletedAt:     instantOfRFC3339(&o.CompletedAt),
			Phase:           string(o.Phase),
			ProgressPercent: o.ProgressPercent,
			Message:         o.Message,
			Error:           o.Error,
		}
	}
	if out.Prerequisites == nil {
		out.Prerequisites = []jellycompat.WebInstallerPrerequisite{}
	}
	return out
}
func registerAdminJellyfinCompatStatus(reg *Registry) {
	op := Operation{Operation: humaOp("GET", Prefix+"/admin/jellyfin-compat/status", "getAdminJellyfinCompatStatus", "admin-settings", "Observe configured compatibility and local web installation state. Installer progress is not a durable cluster-wide job."), Class: ClassActingAdmin, ServiceBacked: true}
	Register(reg, op, func(ctx context.Context, _ *struct{}) (*AdminJellyfinCompatStatusOutput, error) {
		if reg.deps.AdminJellyfinCompatStatus == nil {
			return nil, unavailable("Jellyfin compatibility status")
		}
		status, err := reg.deps.AdminJellyfinCompatStatus.ReadAdminJellyfinCompatStatus(ctx)
		if err != nil {
			return nil, serviceProblem(err)
		}
		return &AdminJellyfinCompatStatusOutput{Body: adminJellyfinCompatStatusOf(status)}, nil
	})
}
