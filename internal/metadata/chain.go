package metadata

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// InstallationEnabledChecker answers whether a plugin installation is enabled.
// It mirrors the structural-dependency style of pluginMetadataResolver /
// PluginResolverAdapter so the metadata package can consult the plugins
// service's in-memory installation cache without importing (and creating an
// import cycle with) the plugins package. *plugins.Service satisfies it via
// IsInstallationEnabled.
type InstallationEnabledChecker interface {
	IsInstallationEnabled(ctx context.Context, installationID int) (bool, error)
}

// installationEnabled reports whether a plugin installation is enabled. When a
// checker is supplied it is served from the plugins service's cache (no DB
// read); when nil it falls back to a direct pool query so tests and non-plugin
// builds keep working unchanged.
func installationEnabled(
	ctx context.Context,
	checker InstallationEnabledChecker,
	pool *pgxpool.Pool,
	installationID int,
) (bool, error) {
	if checker != nil {
		return checker.IsInstallationEnabled(ctx, installationID)
	}
	var enabled bool
	err := pool.QueryRow(ctx,
		"SELECT enabled FROM plugin_installations WHERE id = $1",
		installationID,
	).Scan(&enabled)
	return enabled, err
}

// ChainEntry represents a single entry in a library's provider chain.
type ChainEntry struct {
	PluginInstallationID int
	CapabilityID         string
	CapabilityType       string // always "metadata_provider.v1" for now
	ContentLevel         string // "movie", "series", "season", "episode", or "" (legacy)
	Priority             int
	Enabled              bool
}

// ChainRepository provides operations for the library_provider_chains table.
type ChainRepository struct {
	pool *pgxpool.Pool
}

// NewChainRepository creates a new ChainRepository.
func NewChainRepository(pool *pgxpool.Pool) *ChainRepository {
	return &ChainRepository{pool: pool}
}

// Pool returns the underlying connection pool.
func (r *ChainRepository) Pool() *pgxpool.Pool {
	return r.pool
}

// SetChain replaces the entire provider chain for a given media folder.
func (r *ChainRepository) SetChain(ctx context.Context, folderID int, entries []ChainEntry) error {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("beginning chain transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	_, err = tx.Exec(ctx, "DELETE FROM library_provider_chains WHERE media_folder_id = $1", folderID)
	if err != nil {
		return fmt.Errorf("deleting existing chain: %w", err)
	}

	for _, entry := range entries {
		capType := entry.CapabilityType
		if capType == "" {
			capType = "metadata_provider.v1"
		}
		_, err = tx.Exec(ctx,
			`INSERT INTO library_provider_chains (media_folder_id, plugin_installation_id, capability_id, capability_type, content_level, priority, enabled)
			 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			folderID, entry.PluginInstallationID, entry.CapabilityID, capType, entry.ContentLevel, entry.Priority, entry.Enabled,
		)
		if err != nil {
			return fmt.Errorf("inserting chain entry (install=%d, cap=%s, level=%s, priority=%d): %w",
				entry.PluginInstallationID, entry.CapabilityID, entry.ContentLevel, entry.Priority, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing chain transaction: %w", err)
	}
	return nil
}

// GetChain returns entries for a specific content level. If no per-level entries
// exist, falls back to legacy flat entries (content_level = ”).
func (r *ChainRepository) GetChain(ctx context.Context, folderID int, contentLevel string) ([]ChainEntry, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT plugin_installation_id, capability_id, capability_type, content_level, priority, enabled
		 FROM library_provider_chains
		 WHERE media_folder_id = $1 AND content_level = $2
		 ORDER BY priority ASC`,
		folderID, contentLevel,
	)
	if err != nil {
		return nil, fmt.Errorf("querying chain: %w", err)
	}
	defer rows.Close()

	var entries []ChainEntry
	for rows.Next() {
		var e ChainEntry
		if err := rows.Scan(&e.PluginInstallationID, &e.CapabilityID, &e.CapabilityType, &e.ContentLevel, &e.Priority, &e.Enabled); err != nil {
			return nil, fmt.Errorf("scanning chain entry: %w", err)
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating chain rows: %w", err)
	}

	// Fall back to legacy flat chain if no per-level entries exist.
	if len(entries) == 0 && contentLevel != "" {
		return r.GetChain(ctx, folderID, "")
	}

	if entries == nil {
		entries = []ChainEntry{}
	}
	return entries, nil
}

// GetAllChainEntries returns every chain entry for a folder, across all content levels.
func (r *ChainRepository) GetAllChainEntries(ctx context.Context, folderID int) ([]ChainEntry, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT plugin_installation_id, capability_id, capability_type, content_level, priority, enabled
		 FROM library_provider_chains
		 WHERE media_folder_id = $1
		 ORDER BY content_level, priority ASC`,
		folderID,
	)
	if err != nil {
		return nil, fmt.Errorf("querying all chain entries: %w", err)
	}
	defer rows.Close()

	var entries []ChainEntry
	for rows.Next() {
		var e ChainEntry
		if err := rows.Scan(&e.PluginInstallationID, &e.CapabilityID, &e.CapabilityType, &e.ContentLevel, &e.Priority, &e.Enabled); err != nil {
			return nil, fmt.Errorf("scanning chain entry: %w", err)
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// DeleteChain removes all provider chain entries for a given media folder.
func (r *ChainRepository) DeleteChain(ctx context.Context, folderID int) error {
	_, err := r.pool.Exec(ctx, "DELETE FROM library_provider_chains WHERE media_folder_id = $1", folderID)
	if err != nil {
		return fmt.Errorf("deleting chain: %w", err)
	}
	return nil
}

// AppendProviderToAllChains adds a provider to every existing library chain
// (per content level) that it serves and doesn't already include it. The
// placement callback resolves the plugin's manifest intent for a content level:
// levels it does not support are skipped entirely, and it is appended enabled
// only when it declares the level and opts into default_enabled.
func (r *ChainRepository) AppendProviderToAllChains(
	ctx context.Context,
	pluginInstallationID int,
	capabilityID string,
	placement func(contentLevel string) SeedPlacement,
) error {
	// Find every distinct (folder, level) pair that has chain entries.
	rows, err := r.pool.Query(ctx,
		`SELECT DISTINCT media_folder_id, content_level
		 FROM library_provider_chains`)
	if err != nil {
		return fmt.Errorf("listing chain groups: %w", err)
	}
	defer rows.Close()

	type chainGroup struct {
		folderID     int
		contentLevel string
	}
	var groups []chainGroup
	for rows.Next() {
		var g chainGroup
		if err := rows.Scan(&g.folderID, &g.contentLevel); err != nil {
			return fmt.Errorf("scanning chain group: %w", err)
		}
		groups = append(groups, g)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterating chain groups: %w", err)
	}

	for _, g := range groups {
		// Only attach the provider to levels it actually serves. A single-purpose
		// provider (e.g. audiobook/ebook/manga metadata, or a sports provider that
		// only declares series levels) must not clutter every library's chain with
		// a disabled row for content it cannot handle.
		p := placement(g.contentLevel)
		if !p.SupportsLevel {
			continue
		}

		// Check if the provider is already in this chain.
		var exists bool
		err := r.pool.QueryRow(ctx,
			`SELECT EXISTS(
				SELECT 1 FROM library_provider_chains
				WHERE media_folder_id = $1 AND content_level = $2
				  AND plugin_installation_id = $3 AND capability_id = $4
			)`, g.folderID, g.contentLevel, pluginInstallationID, capabilityID,
		).Scan(&exists)
		if err != nil {
			return fmt.Errorf("checking chain membership: %w", err)
		}
		if exists {
			continue
		}

		// Determine priority position (append after the last entry).
		var maxPriority int
		err = r.pool.QueryRow(ctx,
			`SELECT COALESCE(MAX(priority), -1)
			 FROM library_provider_chains
			 WHERE media_folder_id = $1 AND content_level = $2`,
			g.folderID, g.contentLevel,
		).Scan(&maxPriority)
		if err != nil {
			return fmt.Errorf("getting max priority: %w", err)
		}

		// A provider joins an existing library disabled unless it declares this
		// level and opts into being enabled by default. This keeps a specialist
		// (default_enabled=false) one click away instead of silently taking over
		// established libraries the moment it is installed.
		enabled := p.DefaultPriority > 0 && p.DefaultEnabled

		_, err = r.pool.Exec(ctx,
			`INSERT INTO library_provider_chains (media_folder_id, plugin_installation_id, capability_id, capability_type, content_level, priority, enabled)
			 VALUES ($1, $2, $3, 'metadata_provider.v1', $4, $5, $6)
			 ON CONFLICT DO NOTHING`,
			g.folderID, pluginInstallationID, capabilityID, g.contentLevel, maxPriority+1, enabled,
		)
		if err != nil {
			return fmt.Errorf("appending provider (install=%d, cap=%s) to folder %d level %q: %w",
				pluginInstallationID, capabilityID, g.folderID, g.contentLevel, err)
		}
	}

	return nil
}

// ResolveChain builds the ordered list of Provider implementations for a given
// media folder and content level. If the folder has custom chain entries, those
// are used. Otherwise all enabled metadata provider capabilities are used,
// ordered by their plugin manifest default_priority for the given content level.
//
// Providers whose underlying plugin installation is disabled are silently skipped.
//
// The enabled-check falls back to a direct pool query. Callers on the hot path
// (MetadataService.resolveChainCached) use ResolveChainWithChecker to serve it
// from the plugins service's in-memory installation cache instead.
func ResolveChain(
	ctx context.Context,
	folderID int,
	contentLevel string,
	chainRepo *ChainRepository,
	resolver pluginMetadataResolver,
) ([]Provider, error) {
	return ResolveChainWithChecker(ctx, folderID, contentLevel, chainRepo, resolver, nil)
}

// ResolveChainWithChecker is ResolveChain with an optional
// InstallationEnabledChecker threaded through the enabled-check. A nil checker
// preserves the direct pool-query behavior, so existing callers and tests are
// unaffected.
func ResolveChainWithChecker(
	ctx context.Context,
	folderID int,
	contentLevel string,
	chainRepo *ChainRepository,
	resolver pluginMetadataResolver,
	checker InstallationEnabledChecker,
) ([]Provider, error) {
	chainEntries, err := chainRepo.GetChain(ctx, folderID, contentLevel)
	if err != nil {
		return nil, fmt.Errorf("getting chain for folder %d: %w", folderID, err)
	}

	// Filter to only enabled entries.
	var enabledEntries []ChainEntry
	for _, e := range chainEntries {
		if e.Enabled {
			enabledEntries = append(enabledEntries, e)
		}
	}
	chainEntries = enabledEntries

	if len(chainEntries) > 0 {
		return resolveChainEntries(ctx, chainEntries, resolver, chainRepo.pool, checker), nil
	}

	providers, err := resolveEnabledProvidersByPriority(ctx, contentLevel, resolver, chainRepo.pool, checker)
	if err != nil {
		return nil, err
	}
	return providers, nil
}

// CapabilityInfo holds the fields needed to construct a provider from plugin tables.
type CapabilityInfo struct {
	PluginInstallationID int
	CapabilityID         string
	DisplayName          string
}

// resolveEnabledProviders returns all enabled providers in installation ID order.
// Used by callers that don't have a content-level context (e.g. person refresh).
func resolveEnabledProviders(
	ctx context.Context,
	resolver pluginMetadataResolver,
	pool *pgxpool.Pool,
	checker InstallationEnabledChecker,
) ([]Provider, error) {
	caps, err := ListEnabledMetadataCapabilities(ctx, pool)
	if err != nil {
		return nil, err
	}
	// Exclude capabilities that opt out of default-enable (e.g. the seeded-
	// disabled built-in NFO provider). Like the chain-less fallback, an
	// installed-but-off provider must not activate itself through this
	// content-level-agnostic path (person refresh); only an admin enabling its
	// chain row does. Absent/unparseable metadata stays eligible (legacy).
	eligible := make([]CapabilityInfo, 0, len(caps))
	for _, c := range caps {
		if !extractDefaultEnabled(lookupCapabilityMetadata(ctx, pool, c.PluginInstallationID, c.CapabilityID)) {
			continue
		}
		eligible = append(eligible, c)
	}
	return buildProviders(ctx, eligible, resolver, pool, checker), nil
}

// resolveEnabledProvidersByPriority returns all enabled providers sorted by
// their plugin manifest default_priority for the given content level. Providers
// without a declared priority are placed last (sorted by installation ID as a tiebreaker).
func resolveEnabledProvidersByPriority(
	ctx context.Context,
	contentLevel string,
	resolver pluginMetadataResolver,
	pool *pgxpool.Pool,
	checker InstallationEnabledChecker,
) ([]Provider, error) {
	caps, err := ListEnabledMetadataCapabilities(ctx, pool)
	if err != nil {
		return nil, err
	}

	type ranked struct {
		cap      CapabilityInfo
		priority int
	}

	// Only providers that support contentLevel participate in the chain-less
	// fallback. A provider declaring a non-empty default_priority map that omits
	// this level is excluded outright (not merely ranked last), so a
	// single-purpose provider is never invoked for content it does not handle.
	items := make([]ranked, 0, len(caps))
	for _, c := range caps {
		metadataJSON := lookupCapabilityMetadata(ctx, pool, c.PluginInstallationID, c.CapabilityID)
		if !providerEligibleForFallback(metadataJSON, contentLevel) {
			slog.DebugContext(ctx, "skipping metadata provider: not eligible for chain-less fallback", "component", "metadata",
				"installation_id", c.PluginInstallationID,
				"capability_id", c.CapabilityID,
				"content_level", contentLevel)
			continue
		}
		items = append(items, ranked{cap: c, priority: extractDefaultPriority(metadataJSON, contentLevel)})
	}

	sort.SliceStable(items, func(i, j int) bool {
		pi, pj := items[i].priority, items[j].priority
		if (pi == 0) != (pj == 0) {
			return pi != 0
		}
		if pi != pj {
			return pi < pj
		}
		return items[i].cap.PluginInstallationID < items[j].cap.PluginInstallationID
	})

	sorted := make([]CapabilityInfo, len(items))
	for i, item := range items {
		sorted[i] = item.cap
	}
	return buildProviders(ctx, sorted, resolver, pool, checker), nil
}

// providerSupportsLevel reports whether a metadata provider should participate
// in the chain-less global fallback for contentLevel. A provider that declares
// a non-empty default_priority map is treated as enumerating the content levels
// it supports: it is eligible only for levels present in that map with a
// positive priority. A provider that declares no default_priority makes no
// claim and stays eligible for every level (legacy behavior), ranked last.
//
// This is what keeps a single-purpose provider (e.g. an audiobook metadata
// provider declaring only {"audiobook": N}) from being pulled into video
// content levels when a library has no enabled chain entry for that level.
func providerSupportsLevel(metadataJSON []byte, contentLevel string) bool {
	levels, declared := declaredPriorityLevels(metadataJSON)
	if !declared {
		return true
	}
	return levels[contentLevel] > 0
}

// providerEligibleForFallback reports whether a capability may participate in
// the chain-less fallback for contentLevel. The fallback runs not only for
// chain-less libraries but whenever a level's enabled-filter yields zero
// entries, so a capability that opts out of being enabled by default
// (default_enabled=false — e.g. the seeded-disabled built-in NFO provider or a
// specialist plugin) must not activate itself through it. Absent or
// unparseable metadata defaults to eligible, preserving legacy behavior.
func providerEligibleForFallback(metadataJSON []byte, contentLevel string) bool {
	return providerSupportsLevel(metadataJSON, contentLevel) && extractDefaultEnabled(metadataJSON)
}

// lookupCapabilityMetadata returns the raw plugin_capabilities.metadata JSON for
// a provider's exact metadata_provider.v1 capability, or nil if absent.
func lookupCapabilityMetadata(ctx context.Context, pool *pgxpool.Pool, pluginInstallationID int, capabilityID string) []byte {
	var metadataJSON []byte
	err := pool.QueryRow(ctx,
		`SELECT metadata FROM plugin_capabilities
		 WHERE plugin_installation_id = $1
		   AND capability_id = $2
		   AND capability_type = 'metadata_provider.v1'`,
		pluginInstallationID,
		capabilityID,
	).Scan(&metadataJSON)
	if err != nil {
		return nil
	}
	return metadataJSON
}

// LookupDefaultPriority queries plugin_capabilities for a provider's declared
// default_priority at the given content level. Returns 0 if not found.
func LookupDefaultPriority(ctx context.Context, pool *pgxpool.Pool, pluginInstallationID int, capabilityID, contentLevel string) int {
	return extractDefaultPriority(lookupCapabilityMetadata(ctx, pool, pluginInstallationID, capabilityID), contentLevel)
}

// extractDefaultPriority parses the default_priority for a content level from
// capability metadata JSON.
func extractDefaultPriority(metadataJSON []byte, contentLevel string) int {
	levels, _ := declaredPriorityLevels(metadataJSON)
	if v, ok := levels[contentLevel]; ok && v > 0 {
		return int(v)
	}
	return 0
}

// extractDefaultEnabled reports whether a provider should be enabled by default
// when a chain is first seeded for a new library. It defaults to true when the
// flag is absent or unparseable, so every plugin predating this flag keeps its
// original seeded-enabled behavior. A specialist provider (e.g. a sports
// metadata source that should not compete with general providers on every
// library) sets metadata.default_enabled=false to be seeded installed-but-off,
// leaving it one click away for users who want it. The flag lives in the same
// "metadata" envelope as default_priority.
func extractDefaultEnabled(metadataJSON []byte) bool {
	var meta map[string]json.RawMessage
	if err := json.Unmarshal(metadataJSON, &meta); err != nil {
		return true
	}
	raw, ok := meta["default_enabled"]
	if !ok {
		if innerRaw, innerOK := meta["metadata"]; innerOK {
			var inner map[string]json.RawMessage
			if err := json.Unmarshal(innerRaw, &inner); err == nil {
				raw, ok = inner["default_enabled"]
			}
		}
	}
	if !ok {
		return true
	}
	var enabled bool
	if err := json.Unmarshal(raw, &enabled); err != nil {
		return true
	}
	return enabled
}

// SeedPlacement captures how a provider's manifest wants it placed when a chain
// is first seeded for a content level: whether it handles the level at all, its
// declared priority, and whether it should be enabled by default.
type SeedPlacement struct {
	SupportsLevel   bool
	DefaultPriority int
	DefaultEnabled  bool
}

// LookupSeedPlacement resolves a provider's seed placement for one content level
// from its capability manifest with a single metadata fetch. SupportsLevel uses
// the same rule as the chain-less fallback (providerSupportsLevel): a provider
// that enumerates levels is eligible only for the ones it lists; a provider that
// declares none stays eligible everywhere.
func LookupSeedPlacement(ctx context.Context, pool *pgxpool.Pool, pluginInstallationID int, capabilityID, contentLevel string) SeedPlacement {
	md := lookupCapabilityMetadata(ctx, pool, pluginInstallationID, capabilityID)
	return SeedPlacement{
		SupportsLevel:   providerSupportsLevel(md, contentLevel),
		DefaultPriority: extractDefaultPriority(md, contentLevel),
		DefaultEnabled:  extractDefaultEnabled(md),
	}
}

// declaredPriorityLevels parses a capability's default_priority map. The map may
// sit at the top level or inside a "metadata" envelope (plugin capability
// metadata wraps plugin-declared fields in a "metadata" sub-object). The second
// return value is true only when a non-empty map was found, i.e. the provider
// explicitly enumerates the content levels it supports.
func declaredPriorityLevels(metadataJSON []byte) (map[string]float64, bool) {
	var meta map[string]json.RawMessage
	if err := json.Unmarshal(metadataJSON, &meta); err != nil {
		return nil, false
	}
	dpRaw, ok := meta["default_priority"]
	if !ok {
		if innerRaw, innerOK := meta["metadata"]; innerOK {
			var inner map[string]json.RawMessage
			if err := json.Unmarshal(innerRaw, &inner); err == nil {
				dpRaw, ok = inner["default_priority"]
			}
		}
	}
	if !ok {
		return nil, false
	}
	var dpMap map[string]float64
	if err := json.Unmarshal(dpRaw, &dpMap); err != nil {
		return nil, false
	}
	return dpMap, len(dpMap) > 0
}

// ListEnabledMetadataCapabilities returns all metadata_provider.v1 capabilities
// whose plugin installation is enabled.
func ListEnabledMetadataCapabilities(ctx context.Context, pool *pgxpool.Pool) ([]CapabilityInfo, error) {
	rows, err := pool.Query(ctx,
		`SELECT pc.plugin_installation_id, pc.capability_id,
		        COALESCE(pc.metadata->>'display_name', pc.capability_id)
		 FROM plugin_capabilities pc
		 JOIN plugin_installations pi ON pi.id = pc.plugin_installation_id
		 WHERE pc.capability_type = 'metadata_provider.v1'
		   AND pi.enabled = true
		 ORDER BY pc.plugin_installation_id`)
	if err != nil {
		return nil, fmt.Errorf("listing enabled metadata capabilities: %w", err)
	}
	defer rows.Close()

	var caps []CapabilityInfo
	for rows.Next() {
		var c CapabilityInfo
		if err := rows.Scan(&c.PluginInstallationID, &c.CapabilityID, &c.DisplayName); err != nil {
			return nil, fmt.Errorf("scanning capability: %w", err)
		}
		caps = append(caps, c)
	}
	return caps, rows.Err()
}

// resolveChainEntries builds Provider instances from explicit chain entries,
// skipping providers whose plugin installation is disabled.
func resolveChainEntries(
	ctx context.Context,
	entries []ChainEntry,
	resolver pluginMetadataResolver,
	pool *pgxpool.Pool,
	checker InstallationEnabledChecker,
) []Provider {
	caps := make([]CapabilityInfo, 0, len(entries))
	for _, e := range entries {
		displayName := lookupCapabilityDisplayName(ctx, pool, e.PluginInstallationID, e.CapabilityID)
		caps = append(caps, CapabilityInfo{
			PluginInstallationID: e.PluginInstallationID,
			CapabilityID:         e.CapabilityID,
			DisplayName:          displayName,
		})
	}
	return buildProviders(ctx, caps, resolver, pool, checker)
}

// buildProviders constructs Provider instances from capability info, skipping
// providers whose plugin installation is disabled.
func buildProviders(
	ctx context.Context,
	caps []CapabilityInfo,
	resolver pluginMetadataResolver,
	pool *pgxpool.Pool,
	checker InstallationEnabledChecker,
) []Provider {
	providers := make([]Provider, 0, len(caps))
	for _, c := range caps {
		enabled, err := installationEnabled(ctx, checker, pool, c.PluginInstallationID)
		if err != nil {
			slog.WarnContext(ctx, "skipping metadata provider: installation enabled-check failed", "component", "metadata",
				"installation_id", c.PluginInstallationID, "capability_id", c.CapabilityID, "error", err)
			continue
		}
		if !enabled {
			slog.DebugContext(ctx, "skipping metadata provider: plugin installation disabled", "component", "metadata",
				"installation_id", c.PluginInstallationID, "capability_id", c.CapabilityID)
			continue
		}

		// A builtin installation's capabilities resolve to in-process providers
		// from the builtin registry; everything else is a gRPC plugin provider.
		if installationIsBuiltin(ctx, checker, pool, c.PluginInstallationID) {
			provider, ok := builtinProvider(c.CapabilityID)
			if !ok {
				slog.WarnContext(ctx, "skipping builtin metadata provider: no registered constructor", "component", "metadata",
					"installation_id", c.PluginInstallationID, "capability_id", c.CapabilityID)
				continue
			}
			providers = append(providers, provider)
			continue
		}

		provider, err := NewPluginProviderFromCapability(c.PluginInstallationID, c.CapabilityID, c.DisplayName, resolver)
		if err != nil {
			slog.WarnContext(ctx, "skipping metadata provider during chain resolution", "component", "metadata",
				"installation_id", c.PluginInstallationID,
				"capability_id", c.CapabilityID,
				"error", err,
			)
			continue
		}
		providers = append(providers, provider)
	}
	return providers
}

// installationKindChecker is an optional extension of InstallationEnabledChecker
// that serves the installation kind from the plugins service's in-memory cache,
// letting chain resolution identify builtin rows without a per-capability DB
// query on the hot path. *plugins.Service implements it; when the checker does
// not (nil or a test fake), installationIsBuiltin falls back to a pool query.
type installationKindChecker interface {
	InstallationKind(ctx context.Context, installationID int) (string, error)
}

// installationIsBuiltin reports whether a plugin installation row is the
// reserved builtin-host installation (kind='builtin'). It prefers the cached
// checker (no DB read) and falls back to a direct query. Errors (including a
// pre-migration schema without the kind column) fail closed to "plugin" so
// resolution behavior is unchanged.
func installationIsBuiltin(ctx context.Context, checker InstallationEnabledChecker, pool *pgxpool.Pool, installationID int) bool {
	if kc, ok := checker.(installationKindChecker); ok {
		kind, err := kc.InstallationKind(ctx, installationID)
		if err != nil {
			return false
		}
		return kind == "builtin"
	}
	if pool == nil {
		return false
	}
	var kind string
	err := pool.QueryRow(ctx,
		"SELECT kind FROM plugin_installations WHERE id = $1",
		installationID,
	).Scan(&kind)
	if err != nil {
		return false
	}
	return kind == "builtin"
}

// lookupCapabilityDisplayName retrieves the display name from plugin capability metadata.
func lookupCapabilityDisplayName(ctx context.Context, pool *pgxpool.Pool, installationID int, capabilityID string) string {
	var displayName string
	err := pool.QueryRow(ctx,
		`SELECT COALESCE(metadata->>'display_name', $2)
		 FROM plugin_capabilities
		 WHERE plugin_installation_id = $1 AND capability_id = $2 AND capability_type = 'metadata_provider.v1'`,
		installationID, capabilityID,
	).Scan(&displayName)
	if err != nil {
		return capabilityID
	}
	return displayName
}
