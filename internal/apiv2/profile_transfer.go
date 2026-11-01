package apiv2

import (
	"context"
	"encoding/json"
	"errors"
	"mime"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/Silo-Server/silo-server/internal/backup"
)

// ProfileTransferService exports and imports the signed-in profile's own
// data (docs/architecture/backups.md, "Profile export").
type ProfileTransferService interface {
	Capability() backup.ProfileExportCapability
	Export(ctx context.Context, userID int, profileID string) ([]byte, error)
	Import(ctx context.Context, scope backup.ProfileImportScope, data []byte) (*backup.ProfileImportResult, error)
}

// ProfileExportDocument is a silo-profile-export document. Its shape is the
// file format, versioned on its own (docs/architecture/backups.md), not
// this contract, so the schema leaves it open.
type ProfileExportDocument map[string]any

func (ProfileExportDocument) Schema(huma.Registry) *huma.Schema {
	return &huma.Schema{Type: huma.TypeObject, AdditionalProperties: true, Extensions: map[string]any{extExtensionBag: "profile-export-document"},
		Description: "A silo-profile-export document: format, version, server_id, exported_at, profile_name, data (by data type), and items. It never holds account fields, password or PIN hashes, access fields, or allowed libraries."}
}

// ProfileExportCapability describes profile export and import.
type ProfileExportCapability struct {
	Capability
	Format         string   `json:"format" doc:"The document format exports carry and imports accept" example:"silo-profile-export"`
	Version        int      `json:"version" doc:"The newest format version this server writes and reads" example:"1"`
	MaxImportBytes int      `json:"max_import_bytes" doc:"The largest document an import accepts"`
	DataTypes      []string `json:"data_types" doc:"The data types an export carries, in import order"`
}

type ProfileExportCapabilityOutput struct {
	Status       int
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         ProfileExportCapability
}

type ProfileExportOutput struct {
	ContentDisposition string `header:"Content-Disposition"`
	Body               ProfileExportDocument
}

type ProfileImportInput struct {
	Body    ProfileExportDocument
	RawBody []byte
}

// ProfileImportData is what merging one data type did.
type ProfileImportData struct {
	Type      string   `json:"type" enum:"watch_history,ratings,favorites,collections,dismissals,preferences,notifications"`
	Added     int      `json:"added"`
	Updated   int      `json:"updated"`
	Unchanged int      `json:"unchanged"`
	LeftOut   int      `json:"left_out" doc:"Entries left out: items not in this catalog or that the profile cannot see, or references with nothing to map to"`
	Notes     []string `json:"notes"`
}

// ProfileImportResult reports an import.
type ProfileImportResult struct {
	SameServer bool                `json:"same_server" doc:"The export came from this server; library references were kept only then"`
	Data       []ProfileImportData `json:"data"`
	Unmatched  []string            `json:"unmatched" doc:"Titles of items this server's catalog does not hold"`
	Hidden     []string            `json:"hidden" doc:"Titles of items the profile may not see: outside its libraries, or above its content-rating or advisory limits"`
}

type ProfileImportOutput struct{ Body ProfileImportResult }

func registerProfileTransfer(reg *Registry) {
	Register(reg, viewerOperation(humaOp(http.MethodGet, Prefix+"/capabilities/profile-export", "getProfileExportCapability", "profiles",
		"Discover profile export and import: the document format and version, the import size limit, and the data types carried.")),
		func(context.Context, *CapabilityInput) (*ProfileExportCapabilityOutput, error) {
			doc := ProfileExportCapability{Capability: Capability{State: configuredCapabilityState(reg.deps.ProfileTransfer != nil)}, DataTypes: []string{}}
			if svc := reg.deps.ProfileTransfer; svc != nil {
				c := svc.Capability()
				doc.Format, doc.Version, doc.MaxImportBytes, doc.DataTypes = c.Format, c.Version, c.MaxImportBytes, c.DataTypes
			}
			return &ProfileExportCapabilityOutput{Body: doc}, nil
		})

	export := humaOp(http.MethodGet, Prefix+"/profile/export", "exportProfile", "profiles",
		"Download the acting profile's own data as a silo-profile-export document: watch history and progress, ratings, favorites and watchlist, personal collections, dismissals, preferences, and notification preferences. It carries no account fields, password or PIN hashes, or access fields.")
	Register(reg, viewerOperation(export), func(ctx context.Context, _ *struct{}) (*ProfileExportOutput, error) {
		svc := reg.deps.ProfileTransfer
		if svc == nil {
			return nil, unavailable("profile export")
		}
		data, err := svc.Export(ctx, claimsFrom(ctx).UserID, profileFrom(ctx))
		if err != nil {
			return nil, profileTransferProblem(err)
		}
		var doc ProfileExportDocument
		if err := json.Unmarshal(data, &doc); err != nil {
			return nil, serviceProblem(err)
		}
		return &ProfileExportOutput{Body: doc, ContentDisposition: mime.FormatMediaType("attachment", map[string]string{dispositionFilenameParam: "silo-profile.json"})}, nil
	})

	imp := humaOp(http.MethodPost, Prefix+"/profile/import", "importProfile", "profiles",
		"Merge a silo-profile-export document into the acting profile: newer progress and history win, ratings, favorites, and dismissals are added where the profile has none, collections gain missing items, and preferences are overwritten. Nothing is deleted, and importing the same document again changes nothing. The target is always the acting profile; a document carrying account or access fields is refused, and items the profile cannot see are left out. A PIN-protected profile needs its PIN-verified session.")
	imp.Errors = []int{http.StatusForbidden}
	Register(reg, Operation{Operation: imp, Class: ClassProfileScoped, ServiceBacked: true, DemoRestricted: true,
		RetrySafety: RetrySafetyNaturalIdempotent, MaxBodyBytes: backup.MaxProfileImportBytes},
		func(ctx context.Context, in *ProfileImportInput) (*ProfileImportOutput, error) {
			svc := reg.deps.ProfileTransfer
			if svc == nil {
				return nil, unavailable("profile import")
			}
			scope, ok := scopeFrom(ctx)
			if !ok || scope.ProfileID == "" || scope.ProfileID != profileFrom(ctx) {
				return nil, NewProblem(TypeProfileVerificationRequired, "Choose a profile to import into.")
			}
			// An exemption from the PIN check (an API key) is not the PIN:
			// a PIN-protected profile's data changes need the verified session.
			if !scope.ProfileVerified || scope.PINVerificationSkipped {
				return nil, NewProblem(TypeProfileVerificationRequired, "Importing into a PIN-protected profile requires entering its PIN.")
			}
			target := backup.ProfileImportScope{UserID: claimsFrom(ctx).UserID, ProfileID: scope.ProfileID, Maturity: scope.MaturityLimits}
			if scope.LibrariesRestricted {
				target.AllowedLibraryIDs = append(append([]int{}, scope.AllowedLibraryIDs...), scope.HiddenLibraryIDs...)
			}
			res, err := svc.Import(ctx, target, in.RawBody)
			if err != nil {
				return nil, profileTransferProblem(err)
			}
			out := ProfileImportResult{SameServer: res.SameServer, Data: []ProfileImportData{}, Unmatched: nonNilStrings(res.Unmatched), Hidden: nonNilStrings(res.Hidden)}
			for _, d := range res.Data {
				out.Data = append(out.Data, ProfileImportData{Type: d.Type, Added: d.Added, Updated: d.Updated, Unchanged: d.Unchanged, LeftOut: d.LeftOut, Notes: nonNilStrings(d.Notes)})
			}
			return &ProfileImportOutput{Body: out}, nil
		})
}

func profileTransferProblem(err error) error {
	switch {
	case errors.Is(err, backup.ErrProfileNotFound):
		return NewProblem(TypeNotFound, "Profile not found.")
	case errors.Is(err, backup.ErrProfileImportAccessFields), errors.Is(err, backup.ErrProfileExportTooNew):
		return NewProblem(TypeValidationFailed, err.Error())
	case errors.Is(err, backup.ErrProfileImportTooLarge):
		return NewProblem(TypePayloadTooLarge, err.Error())
	}
	if invalid, ok := errors.AsType[*backup.InvalidProfileExportError](err); ok {
		return NewProblem(TypeValidationFailed, invalid.Error())
	}
	return serviceProblem(err)
}
