package plugins

const (
	DefaultRepositoryURL  = "https://raw.githubusercontent.com/Silo-Server/silo-plugins/main/manifest.json"
	DefaultRepositoryName = "Silo maintained"

	ApprovedCommunityRepositoryURL  = "https://raw.githubusercontent.com/Silo-Community/silo-plugins/main/manifest.json"
	ApprovedCommunityRepositoryName = "Approved community"

	OfficialRepositoryManagedKey          = "official"
	ApprovedCommunityRepositoryManagedKey = "approved-community"

	RepositorySourceSilo              = "silo"
	RepositorySourceApprovedCommunity = "approved_community"
	RepositorySourceExternal          = "external"

	IncludeApprovedCommunityPluginsSetting = "plugins.include_approved_community_plugins"
	MigratedApprovedCommunityCountSetting  = "plugins.approved_community_migrated_plugin_count"
)

type managedRepositoryDefinition struct {
	Key         string
	URL         string
	DisplayName string
	SourceKind  string
}

var managedRepositoryDefinitions = []managedRepositoryDefinition{
	{
		Key:         OfficialRepositoryManagedKey,
		URL:         DefaultRepositoryURL,
		DisplayName: DefaultRepositoryName,
		SourceKind:  RepositorySourceSilo,
	},
	{
		Key:         ApprovedCommunityRepositoryManagedKey,
		URL:         ApprovedCommunityRepositoryURL,
		DisplayName: ApprovedCommunityRepositoryName,
		SourceKind:  RepositorySourceApprovedCommunity,
	},
}

var approvedCommunityPluginIDs = map[string]struct{}{
	"silo.requests.arr":   {},
	"silo.requests.seerr": {},
}

func isApprovedCommunityPlugin(pluginID string) bool {
	_, ok := approvedCommunityPluginIDs[pluginID]
	return ok
}

// First-party plugin IDs the code names more than once.
const (
	pluginIDTheIntroDB = "silo.theintrodb"
	pluginIDTVDB       = "silo.tvdb"
	pluginIDSimkl      = "silo.watchprovider.simkl"
	pluginIDTrakt      = "silo.watchprovider.trakt"
)

// officialPluginRepositories binds each first-party plugin ID to the only
// repository whose signed release may install under it. A plugin ID listed
// here gets the required signature policy whichever catalog lists it
// (docs/architecture/plugin-trust.md#release-signatures). silo-plugins keeps
// a second copy, which its update-catalog workflow checks before it writes
// an entry; adding a first-party plugin changes both.
var officialPluginRepositories = map[string]string{
	"silo.audiobook-metadata":    "https://github.com/Silo-Server/silo-plugin-metadata-audiobook",
	"silo.auth.ldap":             "https://github.com/Silo-Server/silo-plugin-auth-ldap",
	"silo.auth.oidc":             "https://github.com/Silo-Server/silo-plugin-auth-oidc",
	"silo.autoscan.arr":          "https://github.com/Silo-Server/silo-plugin-autoscan-arr",
	"silo.ebook-metadata":        "https://github.com/Silo-Server/silo-plugin-metadata-ebook",
	"silo.manga-metadata":        "https://github.com/Silo-Server/silo-plugin-metadata-manga",
	"silo.mdblist":               "https://github.com/Silo-Server/silo-plugin-metadata-mdblist",
	"silo.sportarr":              "https://github.com/Silo-Server/silo-plugin-metadata-sportarr",
	pluginIDTheIntroDB:           "https://github.com/Silo-Server/silo-plugin-markers-theintrodb",
	pluginIDTMDB:                 "https://github.com/Silo-Server/silo-plugin-metadata-tmdb",
	pluginIDTVDB:                 "https://github.com/Silo-Server/silo-plugin-metadata-tvdb",
	"silo.watchprovider.floppy":  "https://github.com/Silo-Server/silo-plugin-watchprovider-floppy",
	"silo.watchprovider.mdblist": "https://github.com/Silo-Server/silo-plugin-watchprovider-mdblist",
	pluginIDSimkl:                "https://github.com/Silo-Server/silo-plugin-watchprovider-simkl",
	pluginIDTrakt:                "https://github.com/Silo-Server/silo-plugin-watchprovider-trakt",
}

// officialPluginRepository returns the repository bound to a first-party
// plugin ID.
func officialPluginRepository(pluginID string) (string, bool) {
	repo, ok := officialPluginRepositories[pluginID]
	return repo, ok
}
