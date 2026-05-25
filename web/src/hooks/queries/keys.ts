export interface BrowseParams {
  q: string;
  type: string;
  sort: string;
  order: string;
  offset: number;
  limit: number;
  library_id?: number;
  genre?: string;
  year_min?: number;
  year_max?: number;
  content_rating?: string;
}

export interface InfiniteBrowseParams {
  q: string;
  type: string;
  sort: string;
  order: string;
  limit: number;
  library_id?: number;
  genre?: string;
  year_min?: number;
  year_max?: number;
  content_rating?: string;
}

export interface CatalogParams {
  source: string;
  q?: string;
  title?: string;
  scope?: string;
  section_id?: string;
  library_id?: number;
  collection_id?: string;
  person_id?: string;
  type?: string;
  query_fingerprint?: string;
  include_technical?: boolean;
  include_total?: boolean;
  limit: number;
  offset?: number;
  snapshot?: string;
}

export const itemKeys = {
  all: ["items"] as const,
  details: () => ["items", "detail"] as const,
  detail: (id: string, libraryId?: number) =>
    ["items", "detail", id, libraryId ?? "default"] as const,
  watchDetail: (id: string, fileId?: number, libraryId?: number) =>
    ["items", "watchDetail", id, fileId ?? "default", libraryId ?? "default"] as const,
  browse: (params: BrowseParams) => ["items", "browse", params] as const,
  infiniteBrowse: (params: InfiniteBrowseParams) => ["items", "infiniteBrowse", params] as const,
  filters: () => ["items", "filters"] as const,
};

export const catalogKeys = {
  all: ["catalog"] as const,
  list: (params: CatalogParams) => ["catalog", "list", params] as const,
  filters: (params: Omit<CatalogParams, "limit">) => ["catalog", "filters", params] as const,
  itemDetail: (id: string, libraryId?: number) =>
    ["catalog", "items", id, "detail", libraryId ?? "default"] as const,
  itemVersions: (id: string) => ["catalog", "items", id, "versions"] as const,
  itemEpisodes: (id: string, libraryId?: number) =>
    ["catalog", "items", id, "episodes", libraryId ?? "default"] as const,
  seriesSeasons: (seriesId: string, libraryId?: number) =>
    ["catalog", "series", seriesId, "seasons", libraryId ?? "default"] as const,
  seasonDetail: (seriesId: string, seasonNum: number, libraryId?: number) =>
    [
      "catalog",
      "series",
      seriesId,
      "seasons",
      seasonNum,
      "detail",
      libraryId ?? "default",
    ] as const,
  seasonEpisodes: (seriesId: string, seasonNum: number, libraryId?: number) =>
    [
      "catalog",
      "series",
      seriesId,
      "seasons",
      seasonNum,
      "episodes",
      libraryId ?? "default",
    ] as const,
};

export const favoriteKeys = {
  all: ["favorites"] as const,
  list: () => ["favorites", "list"] as const,
  check: (itemId: string) => ["favorites", "check", itemId] as const,
};

export const watchlistKeys = {
  all: ["watchlist"] as const,
  list: () => ["watchlist", "list"] as const,
  check: (itemId: string) => ["watchlist", "check", itemId] as const,
};

export const historyKeys = {
  all: ["history"] as const,
  list: () => ["history", "list"] as const,
};

export const collectionKeys = {
  all: ["collections"] as const,
  list: () => ["collections", "list"] as const,
  items: (collectionId: string) => ["collections", "items", collectionId] as const,
  preview: (scope: "user" | "admin", fingerprint: string) =>
    ["collections", "preview", scope, fingerprint] as const,
  templates: () => ["collections", "templates"] as const,
  mdblistSearch: (query: string) => ["collections", "mdblist", "search", query] as const,
  mdblistTop: () => ["collections", "mdblist", "top"] as const,
};

export const requestKeys = {
  all: ["requests"] as const,
  status: () => ["requests", "status"] as const,
  discovery: () => ["requests", "discovery"] as const,
  discoverySection: (section: string, page: number) =>
    ["requests", "discovery", section, page] as const,
  discoverStudios: () => ["requests", "discover", "studios"] as const,
  discoverNetworks: () => ["requests", "discover", "networks"] as const,
  discoverGenres: () => ["requests", "discover", "genres"] as const,
  discoverBrowse: (
    kind: "studio" | "network" | "genre",
    slug: string,
    mediaType: string | undefined,
    sort: string,
    page: number,
  ) => ["requests", "discover", "browse", kind, slug, mediaType ?? "", sort, page] as const,
  search: (mediaType: string, query: string, page: number) =>
    ["requests", "search", mediaType, query, page] as const,
  detail: (mediaType: string, tmdbID: number) => ["requests", "detail", mediaType, tmdbID] as const,
  mine: (params: Record<string, unknown>) => ["requests", "mine", params] as const,
};

export const libraryCollectionKeys = {
  all: ["libraryCollections"] as const,
  list: (libraryId: number) => ["libraryCollections", "list", libraryId] as const,
  items: (libraryId: number, collectionId: string) =>
    ["libraryCollections", "items", libraryId, collectionId] as const,
  userContributed: (libraryId: number) =>
    ["libraryCollections", "userContributed", libraryId] as const,
};

export const profileKeys = {
  all: ["profiles"] as const,
  list: () => ["profiles", "list"] as const,
  householdSessions: () => ["profiles", "household", "sessions"] as const,
};

export const personKeys = {
  all: ["people"] as const,
  search: (query: string, limit = 20) => ["people", "search", query, limit] as const,
  detail: (id: string) => ["people", "detail", id] as const,
  catalog: (
    id: string,
    options: {
      type?: string;
      limit?: number;
      offset?: number;
    } = {},
  ) =>
    [
      "people",
      "catalog",
      id,
      options.type ?? "all",
      options.limit ?? 24,
      options.offset ?? 0,
    ] as const,
};

export const episodeKeys = {
  all: ["episodes"] as const,
  seasons: (seriesId: string) => ["episodes", "seasons", seriesId] as const,
  seasonDetail: (seriesId: string, seasonNum: number) =>
    ["episodes", "seasons", seriesId, seasonNum, "detail"] as const,
  bySeason: (seriesId: string, seasonNum: number) => ["episodes", seriesId, seasonNum] as const,
  byItem: (itemId: string) => ["episodes", "item", itemId] as const,
};

export const libraryKeys = {
  all: ["libraries"] as const,
  user: (profileId?: string | null) => ["libraries", "user", profileId ?? "none"] as const,
};

export const libraryPlaybackPreferenceKeys = {
  all: ["libraryPlaybackPreferences"] as const,
  list: (profileId?: string | null) =>
    ["libraryPlaybackPreferences", "list", profileId ?? "none"] as const,
  library: (profileId: string | null | undefined, libraryId: number) =>
    ["libraryPlaybackPreferences", "library", profileId ?? "none", libraryId] as const,
};

export const progressKeys = {
  all: ["progress"] as const,
  list: (status?: string, libraryId?: number) => ["progress", "list", status, libraryId] as const,
};

export const settingsKeys = {
  all: ["settings"] as const,
  list: () => ["settings", "list"] as const,
  detail: (key: string) => ["settings", key] as const,
  deviceDetail: (profileId: string | null | undefined, key: string) =>
    ["settings", "device", profileId ?? "none", key] as const,
  effective: (profileId: string | null | undefined, keys: string[]) =>
    ["settings", "effective", profileId ?? "none", [...keys].sort().join(",")] as const,
  plugins: () => ["settings", "plugins"] as const,
  pluginDetail: (installationId: number) => ["settings", "plugins", installationId] as const,
};

export const historyImportKeys = {
  all: ["history-imports"] as const,
  sources: () => ["history-imports", "sources"] as const,
  runs: (limit = 10) => ["history-imports", "runs", limit] as const,
  run: (id?: string) => ["history-imports", "run", id] as const,
  plexCheck: (sessionId?: string) => ["history-imports", "plex-check", sessionId] as const,
};

export const webhookSyncKeys = {
  all: ["webhook-sync"] as const,
  connections: () => ["webhook-sync", "connections"] as const,
  events: (connectionId?: string) => ["webhook-sync", "events", connectionId] as const,
  profileMappings: (connectionId?: string) =>
    ["webhook-sync", "profile-mappings", connectionId] as const,
  connection: (connectionId?: string) => ["webhook-sync", "connection", connectionId] as const,
};

export const watchProviderKeys = {
  all: ["watch-providers"] as const,
  providers: (profileId?: string | null) =>
    ["watch-providers", profileId ?? "none", "providers"] as const,
  connection: (profileId: string | null | undefined, provider: string) =>
    ["watch-providers", profileId ?? "none", provider, "connection"] as const,
  syncRuns: (profileId: string | null | undefined, provider: string) =>
    ["watch-providers", profileId ?? "none", provider, "sync-runs"] as const,
};

export const sectionKeys = {
  all: ["sections"] as const,
  home: () => ["sections", "home"] as const,
  homeLayout: () => ["sections", "home", "layout"] as const,
  homeRefreshSignal: () => ["sections", "home", "refresh-signal"] as const,
  homeItemsRoot: () => ["sections", "home", "items"] as const,
  homeItems: (sectionId: string) => ["sections", "home", "items", sectionId] as const,
  libraryRoot: () => ["sections", "library"] as const,
  libraryLayout: (libraryId: number) => ["sections", "library", libraryId, "layout"] as const,
  library: (libraryId: number) => ["sections", "library", libraryId] as const,
  libraryItemsRoot: (libraryId: number) => ["sections", "library", libraryId, "items"] as const,
  libraryItems: (libraryId: number, sectionId: string) =>
    ["sections", "library", libraryId, "items", sectionId] as const,
  adminList: (scope: string, libraryId?: number) =>
    ["sections", "admin", scope, libraryId] as const,
  profileOverrides: (scope: string, libraryId?: string) =>
    ["sections", "profile", scope, libraryId] as const,
  profileOverridesRaw: (scope: string, libraryId?: string) =>
    ["sections", "profile", scope, libraryId, "raw"] as const,
};

export const ratingKeys = {
  all: ["ratings"] as const,
  item: (itemId: string) => ["ratings", itemId] as const,
  list: () => ["ratings", "list"] as const,
};

export const subtitleKeys = {
  all: ["subtitles"] as const,
  downloaded: (mediaFileId: number) => ["subtitles", "downloaded", mediaFileId] as const,
};

export const recKeys = {
  all: ["recommendations"] as const,
  forYouMain: () => [...recKeys.all, "for-you", "main"] as const,
  forYouRows: () => [...recKeys.all, "for-you", "rows"] as const,
  similar: (itemId: string) => [...recKeys.all, "similar", itemId] as const,
  becauseWatched: (itemId: string) => [...recKeys.all, "because-watched", itemId] as const,
  similarUsers: () => [...recKeys.all, "similar-users"] as const,
  tasteProfile: () => [...recKeys.all, "taste-profile"] as const,
  discover: () => [...recKeys.all, "discover"] as const,
  section: (kind: string, key?: string) => [...recKeys.all, "section", kind, key ?? ""] as const,
  watchTonight: () => [...recKeys.all, "watch-tonight"] as const,
  watchTonightCards: (mode: string, genres: string[]) =>
    [...recKeys.all, "watch-tonight-cards", mode, ...genres.sort()] as const,
  tasteSeedItems: () => [...recKeys.all, "taste-seed", "items"] as const,
};

export const calendarKeys = {
  all: ["calendar"] as const,
  week: (weekStart: string, filter: string, libraryId?: number) =>
    ["calendar", "week", weekStart, filter, libraryId ?? "all"] as const,
};

export const downloadKeys = {
  all: ["downloads"] as const,
  list: () => ["downloads", "list"] as const,
};

export const themeKeys = {
  all: ["theme"] as const,
  adminCss: () => ["theme", "admin-css"] as const,
  catalogIndex: () => ["theme", "catalog"] as const,
};

export const adminKeys = {
  users: () => ["admin", "users"] as const,
  userDetail: (userId: number) => ["admin", "users", userId] as const,
  userProfiles: (userId?: number) => ["admin", "users", userId, "profiles"] as const,
  userSettings: (userId: number) => ["admin", "users", userId, "settings"] as const,
  userSetting: (userId: number, key: string) =>
    ["admin", "users", userId, "settings", key] as const,
  userDeviceSettings: (userId: number) => ["admin", "users", userId, "deviceSettings"] as const,
  userDeviceSettingsByKey: (userId: number, key: string) =>
    ["admin", "users", userId, "deviceSettings", key] as const,
  userSubtitleDeviceSettings: (userId: number) =>
    ["admin", "users", userId, "deviceSettings", "subtitle_appearance"] as const,
  devices: () => ["admin", "devices"] as const,
  deviceDetail: (userId: number, deviceId: string) =>
    ["admin", "devices", userId, deviceId] as const,
  libraries: () => ["admin", "libraries"] as const,
  libraryRoots: (libraryId?: number, state?: string) =>
    ["admin", "libraries", "roots", libraryId ?? "all", state ?? "all"] as const,
  filesystemBrowse: (path: string) => ["admin", "filesystem", "browse", path] as const,
  librarySkippedRoots: () => ["admin", "libraries", "skippedRoots"] as const,
  staleMediaIDs: () => ["admin", "libraries", "staleMediaIDs"] as const,
  jobs: (jobType?: string) => ["admin", "jobs", jobType] as const,
  catalogImportSources: () => ["admin", "catalog", "importSources"] as const,
  localImportSources: () => ["admin", "catalog", "localImportSources"] as const,
  collections: (libraryId?: number) => ["admin", "collections", libraryId] as const,
  collectionGroups: (libraryId?: number) => ["admin", "collectionGroups", libraryId] as const,
  collectionTemplates: () => ["admin", "collections", "templates"] as const,
  collectionTemplateBundles: () => ["admin", "collections", "templateBundles"] as const,
  libraryProviders: (id: number) => ["admin", "libraries", id, "providers"] as const,
  nodes: () => ["admin", "nodes"] as const,
  stats: () => ["admin", "stats"] as const,
  sessions: () => ["admin", "sessions"] as const,
  serverSettings: () => ["admin", "serverSettings"] as const,
  requestsRoot: () => ["admin", "requests"] as const,
  requests: (params: Record<string, unknown>) => ["admin", "requests", params] as const,
  requestSettings: () => ["admin", "requests", "settings"] as const,
  requestIntegrations: () => ["admin", "requests", "integrations"] as const,
  requestUserLimit: (userId: number) => ["admin", "requests", "users", userId, "limit"] as const,
  recommendationsStatus: () => ["admin", "recommendationsStatus"] as const,
  inviteCodes: () => ["admin", "inviteCodes"] as const,
  apiKeys: () => ["admin", "apiKeys"] as const,
  rateLimitConfig: () => ["admin", "rateLimitConfig"] as const,
  playbackHistory: (params: {
    userId?: number;
    profileId?: string;
    mediaItemId?: string;
    completed?: string;
    limit?: number;
  }) => ["admin", "playbackHistory", params] as const,
  userIPs: (userId: number, days?: number) => ["admin", "users", userId, "ips", days] as const,
  ipUsers: (ip: string, days?: number) => ["admin", "ips", ip, days] as const,
  operationalLogs: (params: Record<string, unknown>) => ["admin", "logs", "app", params] as const,
  auditLogs: (params: Record<string, unknown>) => ["admin", "logs", "audit", params] as const,
  subtitleProviders: () => ["admin", "subtitleProviders"] as const,
  historyImportSources: () => ["admin", "historyImportSources"] as const,
  historyImportExternalUsers: (sourceId: number) =>
    ["admin", "historyImportSources", sourceId, "users"] as const,
  historyImportMappings: (sourceId?: number) =>
    ["admin", "historyImportMappings", sourceId] as const,
  historyImportAdminRuns: (params?: Record<string, unknown>) =>
    ["admin", "historyImportAdminRuns", params] as const,
  historyImportAdminRun: (id?: string) =>
    ["admin", "historyImportAdminRuns", "detail", id] as const,
  activeScans: () => ["admin", "activeScans"] as const,
  tasks: () => ["admin", "tasks"] as const,
  task: (key: string) => ["admin", "tasks", key] as const,
  taskHistory: (key: string) => ["admin", "tasks", key, "history"] as const,
  taskMetrics: (key: string) => ["admin", "tasks", key, "metrics"] as const,
  pluginRepositories: () => ["admin", "plugins", "repositories"] as const,
  pluginCatalog: () => ["admin", "plugins", "catalog"] as const,
  pluginInstallations: () => ["admin", "plugins", "installations"] as const,
  unmatchedItems: (page?: number) =>
    page != null
      ? (["admin", "libraries", "unmatchedItems", page] as const)
      : (["admin", "libraries", "unmatchedItems"] as const),
  itemImages: (id: string) => ["admin", "items", id, "images"] as const,
  buildInfo: () => ["admin", "system", "buildInfo"] as const,
  hwAccel: () => ["admin", "system", "hwAccel"] as const,
};
