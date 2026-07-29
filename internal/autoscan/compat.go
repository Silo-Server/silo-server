package autoscan

import "strings"

// Compatibility descriptors for first-party scan-source plugins that predate
// the descriptor contract.
//
// These exist so the admin UI can be fully descriptor-driven today, before
// every plugin has shipped a manifest that declares its own setup contract.
// Each entry is a stopgap with a clear exit: once the plugin publishes the same
// information in its manifest, its entry here can be deleted with no UI change,
// because the manifest value wins over the compatibility value.
//
// This file is deliberately the *only* place in the host that maps a plugin id
// to setup behavior. It replaces per-plugin conditionals that were previously
// scattered through the admin UI.

const (
	// cephFSPluginID and cephFSCapabilityID identify the first-party CephFS
	// watcher. It reads a mounted filesystem directly, so it needs no upstream
	// credentials.
	cephFSPluginID     = "silo.autoscan.cephfs"
	cephFSCapabilityID = "cephfs"

	// CephFSMoviePathsKey and CephFSTVPathsKey are the source_config keys the
	// CephFS watcher reads its watch roots from. They are named here only to
	// build the compatibility form; the host does not interpret their values.
	CephFSMoviePathsKey = "movie_flat_paths"
	CephFSTVPathsKey    = "tv_flat_paths"
	// CephFSExclusionsKey holds newline-separated path fragments to ignore.
	CephFSExclusionsKey = "exclusions"
)

// defaultCephFSExclusions are the ignore patterns the admin UI used to seed by
// hand. They cover partial downloads and NAS bookkeeping directories that would
// otherwise trigger pointless scans.
var defaultCephFSExclusions = []string{
	"*.partial",
	"*.tmp",
	"@eaDir",
	"#recycle",
	".downloads",
	".recyclebin",
	"volumes",
}

// cephFSCompatibilityDescriptor is the setup contract the CephFS watcher would
// declare in its own manifest. Field keys match what the plugin already reads
// from source_config, so existing sources keep working untouched.
func cephFSCompatibilityDescriptor() ScanSourceDescriptor {
	return ScanSourceDescriptor{
		DeliveryModes: []string{DeliveryModePoll},
		Connection:    ConnectionNone,
		Summary:       "Watch mounted CephFS paths for new and changed media.",
		ConfigForm: &AdminForm{
			Fields: []AdminFormField{
				{
					Key:         CephFSMoviePathsKey,
					Label:       "Movie paths",
					Description: "One path per line. Leave blank if this watcher only covers TV.",
					Control:     ControlTextarea,
					Placeholder: "/mnt/media/movies",
					Multiline:   true,
					Rows:        4,
					FillFrom:    FillFromMovieLibraryPaths,
				},
				{
					Key:         CephFSTVPathsKey,
					Label:       "TV paths",
					Description: "One path per line. Leave blank if this watcher only covers movies.",
					Control:     ControlTextarea,
					Placeholder: "/mnt/media/tv",
					Multiline:   true,
					Rows:        4,
					FillFrom:    FillFromTVLibraryPaths,
				},
				{
					Key:          CephFSExclusionsKey,
					Label:        "Exclusions",
					Description:  "Path fragments to ignore, one per line.",
					Control:      ControlTextarea,
					Multiline:    true,
					Rows:         6,
					DefaultValue: strings.Join(defaultCephFSExclusions, "\n"),
				},
			},
		},
	}
}

// ApplyCompatibilityDescriptor fills gaps in a plugin-declared descriptor from
// a host-side stopgap for known first-party plugins.
//
// The manifest always wins: only fields the plugin left unset are filled in.
// That ordering is what lets a plugin take ownership of its own contract simply
// by publishing it, with no coordinated host change.
func ApplyCompatibilityDescriptor(pluginID, capabilityID string, declared ScanSourceDescriptor) ScanSourceDescriptor {
	compat, ok := compatibilityDescriptor(pluginID, capabilityID)
	if !ok {
		return declared
	}

	// Fill only what the manifest did not state. Comparing values would be
	// wrong: a plugin that explicitly declares a value equal to a host default
	// is indistinguishable from one that said nothing, so the explicit choice
	// would be silently overwritten. DescriptorFromMetadata records which fields
	// were actually present, which is what makes "manifest wins" true.
	if !declared.Declared(fieldDeliveryModes) && len(compat.DeliveryModes) > 0 {
		declared.DeliveryModes = compat.DeliveryModes
	}
	if !declared.Declared(fieldConnection) && compat.Connection != "" {
		declared.Connection = compat.Connection
	}
	if !declared.Declared(fieldConnectionKinds) {
		declared.ConnectionKinds = compat.ConnectionKinds
	}
	if !declared.Declared(fieldEmitsNativePaths) {
		declared.EmitsNativePaths = compat.EmitsNativePaths
	}
	if !declared.Declared(fieldSummary) {
		declared.Summary = compat.Summary
	}
	if !declared.Declared(fieldIconURL) {
		declared.IconURL = compat.IconURL
	}
	if !declared.Declared(fieldConfigForm) {
		declared.ConfigForm = compat.ConfigForm
	}

	return declared
}

// compatibilityDescriptor returns the stopgap descriptor for a known
// first-party plugin, if one exists.
func compatibilityDescriptor(pluginID, capabilityID string) (ScanSourceDescriptor, bool) {
	// Both must match. Capability ids are chosen by plugin authors and are not
	// unique across plugins, so an OR here would hand CephFS's path/exclusion
	// form to any unrelated plugin that happened to name a capability "cephfs".
	if pluginID == cephFSPluginID && capabilityID == cephFSCapabilityID {
		return cephFSCompatibilityDescriptor(), true
	}
	return ScanSourceDescriptor{}, false
}
