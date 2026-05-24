import { getDefaultQuerySortOrder, normalizeQuerySortField } from "@/lib/querySortOptions";

// Auth
export interface LoginRequest {
  username: string;
  password: string;
  provider?: string;
}

export interface LoginResponse {
  access_token: string;
  refresh_token: string;
  expires_in: number;
  user: User;
}

export interface DeviceLoginStartResponse {
  device_code: string;
  user_code: string;
  match_code: string;
  verification_uri: string;
  verification_uri_complete: string;
  expires_at: string;
  expires_in: number;
  interval: number;
  device_name: string;
  device_platform: string;
}

export interface DeviceLoginLookupResponse {
  status: "pending" | "approved" | "denied" | "expired" | "consumed";
  user_code?: string;
  match_code?: string;
  device_name?: string;
  device_platform?: string;
  ip_address_hint?: string;
  expires_at?: string;
}

export interface DeviceLoginPollResponse {
  status: "pending" | "approved" | "denied" | "expired" | "consumed";
  poll_after: number;
  access_token?: string;
  refresh_token?: string;
  expires_in?: number;
  user?: User;
}

export interface RefreshRequest {
  refresh_token: string;
}

export interface RefreshResponse {
  access_token: string;
  refresh_token: string;
  expires_in: number;
}

export interface AuthProviderOption {
  id: string;
  display_name: string;
  mode: string;
  default: boolean;
  icon_url?: string;
  installation_id?: number;
}

export interface SetupStatusResponse {
  needs_setup: boolean;
}

export interface SetupRequest {
  username: string;
  email: string;
  password: string;
  create_default_profile?: boolean;
  default_profile_name?: string;
}

export interface SignupRequest {
  username: string;
  email: string;
  password: string;
  invite_code: string;
  create_default_profile?: boolean;
  default_profile_name?: string;
}

export interface ImpersonationInfo {
  active: boolean;
  impersonator_user_id: number;
  impersonator_username: string;
}

export interface User {
  id: number;
  username: string;
  email: string;
  role: string;
  download_allowed: boolean;
  impersonation?: ImpersonationInfo | null;
}

export interface AuthSession {
  id: string;
  device_name: string;
  ip_address: string;
  created_at: string;
  expires_at: string;
  revoked_at: string | null;
}

// Profiles
export interface Profile {
  id: string;
  name: string;
  avatar: string;
  avatar_url?: string;
  avatar_source?: "preset" | "upload" | "none";
  has_pin: boolean;
  is_child: boolean;
  is_primary: boolean;
  max_content_rating: string;
  quality_preference: string;
  language: string;
  subtitle_language: string;
  subtitle_mode: string;
  show_forced_subtitles?: boolean;
  auto_skip_intro: boolean;
  auto_skip_credits: boolean;
  auto_skip_recap: boolean;
  auto_play_next_preview: boolean;
  library_restrictions_enabled: boolean;
  allowed_library_ids: number[] | null;
  max_playback_quality: string;
  created_at: string;
  updated_at: string;
}

export interface ProfileListResponse {
  profiles: Profile[];
  avatar_upload_enabled: boolean;
}

export interface CreateProfileRequest {
  name: string;
  avatar?: string;
  pin?: string;
  is_child?: boolean;
  max_content_rating?: string;
  quality_preference?: string;
  language?: string;
  subtitle_language?: string;
  subtitle_mode?: string;
  show_forced_subtitles?: boolean;
  auto_skip_intro?: boolean;
  auto_skip_credits?: boolean;
  auto_skip_recap?: boolean;
  auto_play_next_preview?: boolean;
  library_restrictions_enabled?: boolean;
  allowed_library_ids?: number[] | null;
  max_playback_quality?: string;
}

export interface UpdateProfileRequest extends Partial<CreateProfileRequest> {}

export interface VerifyPinResponse {
  valid: boolean;
  profile_token?: string;
  expires_at?: string;
}

// History Import
export interface HistoryImportSource {
  id: number;
  name: string;
  source_type: string;
  base_url?: string;
  system_id?: string;
  enabled: boolean;
  sort_order: number;
  has_admin_token: boolean;
  created_at: string;
  updated_at: string;
}

export interface HistoryImportConnectServer {
  server_id: string;
  name: string;
  system_id?: string;
  has_remote_url: boolean;
  has_local_address: boolean;
}

export interface EmbyConnectLoginRequest {
  username: string;
  password: string;
}

export interface EmbyConnectLoginResponse {
  connect_session_id: string;
  servers: HistoryImportConnectServer[];
  expires_at: string;
}

export interface PlexPinResponse {
  session_id: string;
  pin_code: string;
  auth_url: string;
  expires_at: string;
}

export interface PlexCheckRequest {
  session_id: string;
}

export interface PlexCheckResponse {
  authenticated: boolean;
  servers?: PlexServer[];
}

export interface PlexServer {
  name: string;
  client_identifier: string;
  owned: boolean;
  has_remote_url: boolean;
  has_local_url: boolean;
}

export interface WebhookSyncConnection {
  id: string;
  user_id?: number;
  provider: "plex" | "emby" | "jellyfin";
  server_id: string;
  server_name: string;
  default_profile_id: string;
  webhook_url?: string;
  actor_count?: number;
  account_discovery_available?: boolean;
  last_webhook_received_at?: string | null;
  last_webhook_error_at?: string | null;
  last_webhook_error_message?: string | null;
  created_at?: string;
  updated_at?: string;
}

export interface WebhookSyncEventLog {
  id: number;
  connection_id?: string;
  received_at: string;
  request_id?: string;
  http_status: number;
  outcome: "applied" | "ignored" | "unmatched" | "skipped" | "rejected" | "error";
  summary: string;
  error_message?: string | null;
  body_excerpt?: string | null;
  attrs?: Record<string, unknown>;
}

export interface WebhookSyncActorMapping {
  id: number;
  connection_id?: string;
  external_actor_id: string;
  external_actor_name: string;
  silo_profile_id?: string | null;
  last_seen_at?: string;
  created_at?: string;
  updated_at?: string;
}

export interface WebhookSyncDiscoveredActor {
  external_actor_id: string;
  external_actor_name: string;
}

export interface WebhookSyncActorsResponse {
  mappings: WebhookSyncActorMapping[];
  discovered_actors?: WebhookSyncDiscoveredActor[];
  account_discovery_available?: boolean;
}

export type CreateWebhookSyncConnectionRequest =
  | {
      provider: "plex";
      server_id: string;
      server_name: string;
      base_url: string;
      access_token: string;
      default_profile_id: string;
    }
  | {
      provider: "emby" | "jellyfin";
      server_name: string;
      default_profile_id: string;
    };

export interface CreateWebhookSyncConnectionResponse {
  connection: WebhookSyncConnection;
  webhook_url: string;
}

export interface RotateWebhookSyncWebhookResponse {
  webhook_url: string;
}

export interface UpdateWebhookSyncConnectionRequest {
  server_name?: string;
  default_profile_id?: string;
}

export interface UpdateWebhookSyncActorsRequest {
  mappings: Array<{
    external_actor_id: string;
    external_actor_name: string;
    silo_profile_id: string | null;
  }>;
}

export interface HistoryImportUnmatchedSample {
  kind: string;
  title: string;
  year?: number;
  reason: string;
}

export interface HistoryImportRun {
  id: string;
  user_id: number;
  profile_id: string;
  source_type: string;
  connection_mode: string;
  status: "queued" | "running" | "completed" | "failed" | "cancelled";
  mapping_id?: number;
  fetched: number;
  matched: number;
  unmatched: number;
  progress_updated: number;
  history_created: number;
  skipped: number;
  warnings: string[];
  unmatched_samples: HistoryImportUnmatchedSample[];
  error_message?: string;
  created_at: string;
  started_at?: string;
  completed_at?: string;
}

export interface ScanRunResult {
  new: number;
  updated: number;
  unchanged: number;
  missing: number;
  files_deleted: number;
  memberships_removed: number;
  items_deleted: number;
  matched_files: number;
  retried_items: number;
  still_unmatched_warnings: number;
  skipped: number;
  errors: number;
  phase?: string;
  message?: string;
  current_scope?: string;
  total_files?: number;
  files_discovered?: number;
  files_processed?: number;
}

export interface ScanRun {
  id: string;
  library_id: number;
  mode: "library" | "subtree" | "file";
  path?: string;
  trigger: string;
  status: "accepted" | "running" | "completed" | "failed" | "cancelled";
  started_at?: string;
  completed_at?: string;
  error_message?: string;
  result?: ScanRunResult;
}

export interface CreateHistoryImportRunRequest {
  profile_id: string;
  source: "emby" | "jellyfin" | "plex";
  connect_session_id?: string;
  server_id?: string;
  source_id?: number;
  username?: string;
  password?: string;
  jellyfin_base_url?: string;
  jellyfin_username?: string;
  jellyfin_password?: string;
  plex_session_id?: string;
  plex_server_id?: string;
  plex_base_url?: string;
  plex_token?: string;
}

export interface CreateHistoryImportSourceRequest {
  name: string;
  source_type: "emby" | "jellyfin" | "plex";
  base_url: string;
  system_id?: string;
  enabled: boolean;
  sort_order: number;
  admin_token?: string;
}

export interface UpdateHistoryImportSourceRequest {
  name?: string;
  base_url?: string;
  system_id?: string;
  enabled?: boolean;
  sort_order?: number;
}

export interface SetHistoryImportAdminTokenRequest {
  token: string;
}

export interface HistoryImportExternalUser {
  id: string;
  name: string;
}

export interface HistoryImportUserMapping {
  id: number;
  source_id: number;
  external_user_id: string;
  external_user_name: string;
  silo_user_id: number;
  silo_profile_id: string;
  silo_username?: string;
  silo_profile_name?: string;
  last_imported_at?: string;
  created_at: string;
  updated_at: string;
}

export interface CreateHistoryImportMappingRequest {
  source_id: number;
  external_user_id: string;
  external_user_name: string;
  silo_user_id: number;
  silo_profile_id: string;
}

export type HistoryRemovalScope = "item" | "show";

export interface HistoryRemovalTargetRequest {
  content_id: string;
  scope: HistoryRemovalScope;
}

export interface RemoveHistoryRequest {
  targets: HistoryRemovalTargetRequest[];
}

export interface UpdateHistoryImportMappingRequest {
  silo_user_id?: number;
  silo_profile_id?: string;
}

export interface AdminHistoryImportBulkRunResult {
  runs: HistoryImportRun[];
  skipped: number;
  errors: number;
}

// Person
export interface Person {
  id: number;
  name: string;
  bio?: string;
  birth_date?: string;
  death_date?: string;
  birthplace?: string;
  homepage?: string;
  photo_url?: string;
  photo_thumbhash?: string;
  tmdb_id?: string;
  imdb_id?: string;
  tvdb_id?: string;
  plex_guid?: string;
}

export interface PersonRefreshQueuedResponse {
  status: string;
  person_id: number;
}

export interface UpdatePersonRequest {
  name?: string;
  bio?: string;
  birth_date?: string | null;
  death_date?: string | null;
  birthplace?: string;
  homepage?: string;
  tmdb_id?: string;
  imdb_id?: string;
  tvdb_id?: string;
}

// Cast & Crew (served inline from API)
export interface CastMember {
  name: string;
  character: string;
  order: number;
  person_id: string;
  tmdb_id?: string;
  tvdb_id?: string;
  imdb_id?: string;
  plex_guid?: string;
  photo_url?: string;
  photo_thumbhash?: string;
}

export interface CrewMember {
  name: string;
  job: string;
  person_id: string;
  tmdb_id?: string;
  tvdb_id?: string;
  imdb_id?: string;
  plex_guid?: string;
  photo_url?: string;
  photo_thumbhash?: string;
}

// Seasons / Watched State
export interface LeafItemUserData {
  played: boolean;
  is_in_progress?: boolean;
  position_seconds?: number;
  duration_seconds?: number;
  last_file_id?: number;
  last_resolution?: string;
  last_hdr?: boolean;
  last_codec_video?: string;
  last_edition_key?: string;
}

export interface SeasonUserData {
  played: boolean;
  watched_count: number;
  unplayed_count: number;
  in_progress_count: number;
}

export type ItemUserData = LeafItemUserData | SeasonUserData;

export interface Season {
  content_id: string;
  season_number: number;
  is_specials: boolean;
  title: string;
  overview: string;
  air_date: string | null;
  episode_count: number;
  poster_url: string;
  poster_thumbhash: string;
  user_data?: SeasonUserData;
}

export interface SeasonsResponse {
  seasons: Season[];
}

export interface SeasonDetailResponse {
  season: Season;
}

// Keep legacy alias for backwards compatibility
export type SeasonSummary = Season;

// Overlay summary from media file analysis
export interface OverlaySummary {
  resolution?: string;
  hdr?: string;
  audio?: string;
  audio_channels?: string;
  video_codec?: string;
  container?: string;
  aspect_ratio?: string;
  release_type?: string;
  edition?: string;
  multi_audio?: boolean;
  multi_sub?: boolean;
}

// Browse
export interface MediaItemUserState {
  played: boolean;
  is_favorite: boolean;
  in_watchlist: boolean;
}

export interface BrowseItem {
  content_id: string;
  type: "movie" | "series" | "season" | "episode";
  title: string;
  series_title?: string;
  season_number?: number | null;
  episode_number?: number | null;
  year: number;
  runtime?: number;
  genres: string[];
  studios?: string[];
  networks?: string[];
  content_rating: string;
  status: "pending" | "matched" | "unmatched" | "ambiguous";
  show_status?: string;
  rating_imdb: number | null;
  rating_tmdb?: number | null;
  rating_rt_critic?: number | null;
  rating_rt_audience?: number | null;
  original_language?: string;
  overview: string;
  poster_url: string;
  poster_thumbhash: string;
  backdrop_url: string;
  backdrop_thumbhash: string;
  added_at?: string;
  release_date?: string | null;
  last_air_date?: string | null;
  overlay_summary?: OverlaySummary | null;
  user_state?: MediaItemUserState;
}

export interface BrowseResponse {
  total: number;
  total_exact?: boolean;
  has_more: boolean;
  items: BrowseItem[];
}

export type CatalogSource =
  | "query"
  | "section"
  | "library_collection"
  | "user_collection"
  | "favorites"
  | "watchlist"
  | "history"
  | "person";

export interface CatalogResponse extends BrowseResponse {
  source?: CatalogSource;
  title?: string;
  snapshot?: string;
}

export interface ItemFiltersResponse {
  genres: string[];
  studios: string[];
  networks: string[];
  countries: string[];
  content_ratings: string[];
}

export interface CatalogFiltersResponse extends ItemFiltersResponse {
  resolutions?: string[];
  audio_languages?: string[];
  subtitle_languages?: string[];
  original_languages?: string[];
}

// Item Detail
export interface FileVersion {
  file_id: number;
  file_name?: string;
  file_path?: string;
  resolution: string;
  codec_video: string;
  codec_audio: string;
  hdr: boolean;
  container: string;
  file_size: number;
  duration: number;
  bitrate: number;
  added_at?: string;
  edition_raw?: string;
  edition_key?: string;
  presentation_kind?: string;
  presentation_group_key?: string;
  presentation_part_index?: number;
  multi_episode_start?: number;
  multi_episode_end?: number;
  effective_audio_track_index?: number;
  effective_audio_language?: string;
  video_tracks?: VersionVideoTrack[];
  audio_tracks?: VersionAudioTrack[];
  subtitle_tracks?: VersionSubtitleTrack[];
  chapters?: VersionChapter[];
  intro?: TimeRange | null;
  credits?: TimeRange | null;
  recap?: TimeRange | null;
  preview?: TimeRange | null;
}

export interface PlaybackVariantPart {
  part_index: number;
  default_file_id?: number;
  total_duration?: number;
  versions: FileVersion[];
}

export interface PlaybackVariant {
  variant_id: string;
  edition_raw?: string;
  edition_key?: string;
  presentation_kind?: string;
  presentation_group_key?: string;
  part_count: number;
  total_duration?: number;
  default_file_id?: number;
  parts: PlaybackVariantPart[];
}

export interface VersionChapter {
  index: number;
  title: string;
  start_seconds: number;
  end_seconds: number;
  source: string;
  thumbnail_url?: string;
  thumbnail_thumbhash?: string;
}

export interface VersionVideoTrack {
  title?: string;
  codec?: string;
  dolby_vision?: string;
  profile?: string;
  level?: number;
  width?: number;
  height?: number;
  aspect_ratio?: string;
  interlaced?: boolean;
  frame_rate?: string;
  bitrate?: number;
  video_range?: string;
  color_primaries?: string;
  color_space?: string;
  color_transfer?: string;
  bit_depth?: number;
  pixel_format?: string;
  reference_frames?: number;
}

export interface VersionAudioTrack {
  title?: string;
  embedded_title?: string;
  language?: string;
  codec?: string;
  layout?: string;
  channels?: number;
  bitrate?: number;
  sample_rate?: number;
  bit_depth?: number;
  default?: boolean;
}

export interface VersionSubtitleTrack {
  index?: number;
  language?: string;
  codec?: string;
  title?: string;
  embedded_title?: string;
  resolution?: string;
  forced?: boolean;
  default?: boolean;
  hearing_impaired?: boolean;
  external?: boolean;
  file_name?: string;
}

export interface SubtitleInfo {
  source: string;
  language: string;
  codec: string;
  forced: boolean;
  hearing_impaired?: boolean;
  title: string;
}

export interface SubtitleTrackSignature {
  source?: string;
  language?: string;
  codec?: string;
  label?: string;
  forced?: boolean;
  hearing_impaired?: boolean;
}

export interface TimeRange {
  start: number;
  end: number;
}

export interface ItemDetail {
  content_id: string;
  type: "movie" | "series" | "season" | "episode";
  status?: "pending" | "matched" | "unmatched" | "ambiguous";

  // Metadata (served inline from Postgres).
  title: string;
  sort_title?: string;
  original_title?: string;
  year: number;
  overview: string;
  tagline?: string;
  runtime: number;
  content_rating: string;
  genres: string[];
  rating_imdb: number | null;
  rating_tmdb: number | null;
  rating_rt_critic: number | null;
  rating_rt_audience: number | null;
  imdb_id: string;
  tmdb_id: string;
  tvdb_id: string;
  cast: CastMember[];
  crew: CrewMember[];
  studios: string[];
  networks: string[];
  countries: string[];
  locked_fields?: number[];
  release_date: string | null;
  first_air_date: string | null;
  last_air_date: string | null;
  air_time?: string | null;

  // Presigned image URLs.
  poster_url: string;
  poster_thumbhash: string;
  backdrop_url: string;
  backdrop_thumbhash: string;
  logo_url: string;

  // Series-specific.
  season_count: number | null;

  // Season-specific.
  series_id?: string;
  series_title?: string;
  season_number?: number | null;
  episode_number?: number | null;
  episode_count?: number | null;
  air_date?: string | null;
  is_specials?: boolean;
  user_data?: ItemUserData;
  user_state?: MediaItemUserState;
  user_rating?: number | null;

  // Root folder paths for series items (admin-only).
  folder_paths?: string[];

  // Playback.
  versions: FileVersion[];
  playback_variants?: PlaybackVariant[];
  subtitles: SubtitleInfo[];
  intro: TimeRange | null;
  credits: TimeRange | null;
  recap?: TimeRange | null;
  preview?: TimeRange | null;
  effective_subtitle_language?: string;
  effective_subtitle_mode?: string;
  effective_show_forced_subtitles?: boolean;
  effective_subtitle_track_signature?: SubtitleTrackSignature;
  effective_version_resolution?: string;
  effective_version_hdr?: boolean;
  effective_version_codec_video?: string;
  effective_version_edition_key?: string;
}

export interface WatchDetail {
  content_id: string;
  type: string;
  title: string;
  year?: number;
  overview: string;
  versions: FileVersion[];
  playback_variants?: PlaybackVariant[];
  subtitles: SubtitleInfo[];
  intro: TimeRange | null;
  credits: TimeRange | null;
  recap?: TimeRange | null;
  preview?: TimeRange | null;
  user_data?: LeafItemUserData;
  series_id?: string;
  series_title?: string;
  season_number?: number;
  episode_number?: number;
  effective_subtitle_language?: string;
  effective_subtitle_mode?: string;
  effective_show_forced_subtitles?: boolean;
  effective_subtitle_track_signature?: SubtitleTrackSignature;
  effective_version_resolution?: string;
  effective_version_hdr?: boolean;
  effective_version_codec_video?: string;
  effective_version_edition_key?: string;
}

// Episodes
export interface Episode {
  content_id: string;
  season_number: number;
  episode_number: number;
  imdb_id?: string;
  tmdb_id?: string;
  tvdb_id?: string;
  still_url: string;
  still_thumbhash: string;
}

export interface EpisodeFile {
  file_id: number;
  resolution: string;
  codec_video: string;
  hdr: boolean;
  audio_channels: number;
  container: string;
  file_size: number;
}

export interface EpisodeListItem {
  content_id: string;
  season_number: number;
  episode_number: number;
  title: string;
  overview: string;
  air_date: string | null;
  runtime: number;
  imdb_id?: string;
  tmdb_id?: string;
  tvdb_id?: string;
  still_url: string;
  still_thumbhash: string;
  user_data?: LeafItemUserData;
  files: EpisodeFile[];
}

export interface EpisodesResponse {
  episodes: EpisodeListItem[];
}

// Collections
export type UserCollectionType = "manual" | "smart" | "mdblist" | "tmdb" | "trakt";

export type UserCollectionSyncStatus = "" | "running" | "success" | "failed" | "warning";
export type GroupSortMode = "manual" | "name_asc" | "name_desc" | "recent" | "most_items";
export type LibraryCollectionGroupKind = "regular" | "user_collections";

export interface Collection {
  id: string;
  profile_id: string;
  creator_profile_id: string;
  name: string;
  description?: string;
  collection_type: UserCollectionType;
  is_shared: boolean;
  allowed_profile_ids: string[];
  query_definition: QueryDefinition;
  sort_config: Record<string, unknown>;
  sort_order: number;
  group_id?: string | null;
  source_url?: string;
  source_config?: Record<string, unknown>;
  sync_schedule?: string;
  next_sync_at?: string;
  last_sync_at?: string;
  last_sync_status?: UserCollectionSyncStatus;
  last_sync_message?: string;
  item_count?: number;
  include_in_server_collections?: boolean;
  poster_url?: string;
  poster_thumbhash?: string;
  created_at: string;
  updated_at: string;
}

export interface ServerVisibleUserCollection {
  id: string;
  creator_profile_id: string;
  name: string;
  description?: string;
  collection_type: UserCollectionType;
  item_count: number;
  poster_url?: string;
  poster_thumbhash?: string;
  created_at: string;
  updated_at: string;
}

export interface CollectionItem {
  collection_id: string;
  media_item_id: string;
  position: number;
  added_at: string;
}

export interface CollectionGroup {
  id: string;
  name: string;
  slug: string;
  default_sort_mode: GroupSortMode;
  sort_order: number;
}

export interface CollectionsListResponse {
  collections: Collection[];
  groups: CollectionGroup[];
}

export interface QueryRule {
  field: string;
  op: string;
  value: string | number | boolean | [string | number, string | number];
}

export interface QueryGroup {
  match: "all" | "any";
  rules: QueryRule[];
}

export interface QuerySort {
  field:
    | "title"
    | "added_at"
    | "release_date"
    | "last_air_date"
    | "year"
    | "content_rating"
    | "runtime"
    | "rating_imdb"
    | "rating_tmdb"
    | "rating_rt_critic"
    | "rating_rt_audience"
    | "resolution"
    | "bitrate"
    | "progress"
    | "date_viewed"
    | "plays"
    | "relevance";
  order: "asc" | "desc";
}

export interface QueryDefinition {
  library_ids: number[];
  media_scope?: "movie" | "series" | "episode";
  match: "all" | "any";
  groups: QueryGroup[];
  sort: QuerySort;
  limit?: number;
}

export interface QueryDefinitionInput {
  library_ids?: number[];
  media_scope?: QueryDefinition["media_scope"] | string;
  match?: QueryDefinition["match"] | string;
  groups?: QueryGroup[];
  sort?: {
    field?: string;
    order?: string;
  } | null;
  limit?: number;
}

export interface SmartCollectionAccess {
  is_shared: boolean;
  allowed_profile_ids: string[];
}

export interface CollectionPreviewRequest {
  query_definition: QueryDefinition;
  limit?: number;
}

export interface CollectionPreviewItem {
  content_id: string;
  title: string;
  type: string;
}

export interface CollectionPreviewResponse {
  items: CollectionPreviewItem[];
  total: number;
}

export interface CreateCollectionRequest {
  name: string;
  collection_type?: "manual" | "smart";
  is_shared?: boolean;
  allowed_profile_ids?: string[];
  query_definition?: QueryDefinition;
  sort_config?: Record<string, unknown>;
  include_in_server_collections?: boolean;
  poster_source_url?: string;
}

export interface UpdateCollectionRequest {
  name?: string;
  description?: string;
  is_shared?: boolean;
  allowed_profile_ids?: string[];
  query_definition?: QueryDefinition;
  sort_config?: Record<string, unknown>;
  source_url?: string;
  /** 0 = unlimited; otherwise a positive cap. */
  max_items?: number;
  include_in_server_collections?: boolean;
  poster_source_url?: string;
  group_id?: string | null;
}

export interface LibraryCollection {
  id: string;
  library_id: number;
  library_ids: number[];
  slug: string;
  title: string;
  description: string;
  collection_type: "manual" | "smart" | "mdblist" | "tmdb" | "trakt";
  visibility: "visible" | "hidden";
  sort_order: number;
  group_id?: string | null;
  featured: boolean;
  poster_url: string;
  backdrop_url: string;
  poster_thumbhash?: string;
  backdrop_thumbhash?: string;
  source_url: string;
  query_definition: QueryDefinition;
  sort_config: Record<string, unknown>;
  source_config: Record<string, unknown>;
  management_mode?: LibraryCollectionManagementMode;
  management_source?: string;
  management_key?: string;
  last_sync_status: "idle" | "running" | "success" | "failed" | "warning";
  last_sync_message: string;
  last_sync_at?: string;
  sync_schedule?: string;
  next_sync_at?: string;
  item_count: number;
  created_at: string;
  updated_at: string;
}

export type LibraryCollectionManagementMode = "manual" | "section" | "template_bundle";

export interface LibraryCollectionGroup {
  id: string;
  library_id: number;
  name: string;
  slug: string;
  kind: LibraryCollectionGroupKind;
  default_sort_mode: GroupSortMode;
  sort_order: number;
}

export interface LibraryCollectionsListResponse {
  collections: LibraryCollection[];
  groups?: LibraryCollectionGroup[];
}

export interface LibraryCollectionSyncRun {
  id: string;
  collection_id: string;
  status: "running" | "success" | "failed" | "warning";
  message: string;
  items_added: number;
  items_removed: number;
  items_matched: number;
  items_unmatched: number;
  warnings: string[];
  started_at?: string;
  completed_at?: string;
  created_at: string;
}

export interface CreateLibraryCollectionRequest {
  library_id?: number;
  library_ids?: number[];
  slug?: string;
  title: string;
  description?: string;
  collection_type?: "manual" | "smart" | "mdblist" | "tmdb" | "trakt";
  visibility?: "visible" | "hidden";
  sort_order?: number;
  group_id?: string | null;
  featured?: boolean;
  poster_url?: string;
  backdrop_url?: string;
  poster_source_url?: string;
  backdrop_source_url?: string;
  source_url?: string;
  query_definition?: QueryDefinition;
  sort_config?: Record<string, unknown>;
  source_config?: Record<string, unknown>;
  management_mode?: LibraryCollectionManagementMode;
  management_source?: string;
  management_key?: string;
  sync_schedule?: string;
}

export interface UpdateLibraryCollectionRequest extends Partial<CreateLibraryCollectionRequest> {}

export interface LibraryTabCollection {
  id: string;
  title: string;
  poster_url: string;
  poster_thumbhash?: string;
  item_count: number;
  featured?: boolean;
  creator_profile_id?: string | null;
}

export interface LibraryTabGroup {
  id: string;
  name: string;
  kind: LibraryCollectionGroupKind;
  sort_mode: GroupSortMode;
  sort_order: number;
  collections: LibraryTabCollection[];
}

export interface LibraryTabUngrouped {
  sort_order: number;
  collections: LibraryTabCollection[];
}

export interface LibraryTabResponse {
  library_id: number;
  groups: LibraryTabGroup[];
  ungrouped?: LibraryTabUngrouped;
}

export interface ImportMDBListCollectionRequest {
  library_id?: number;
  library_ids?: number[];
  title: string;
  description?: string;
  url: string;
  limit?: number;
  featured?: boolean;
  poster_url?: string;
  poster_source_url?: string;
  backdrop_source_url?: string;
  sync_schedule?: string;
  management_mode?: LibraryCollectionManagementMode;
  management_source?: string;
  management_key?: string;
}

export interface ImportMDBListCollectionResponse {
  collection: LibraryCollection;
  sync_run?: LibraryCollectionSyncRun;
}

export interface ImportTMDBCollectionRequest {
  library_id?: number;
  library_ids?: number[];
  title: string;
  description?: string;
  preset:
    | "trending"
    | "popular"
    | "top_rated"
    | "now_playing"
    | "upcoming"
    | "airing_today"
    | "on_the_air";
  time_window?: "day" | "week";
  media_type: "movie" | "tv" | "all";
  limit?: number;
  featured?: boolean;
  poster_url?: string;
  poster_source_url?: string;
  backdrop_source_url?: string;
  sync_schedule?: string;
  management_mode?: LibraryCollectionManagementMode;
  management_source?: string;
  management_key?: string;
}

export interface ImportTMDBCollectionResponse {
  collection: LibraryCollection;
  sync_run?: LibraryCollectionSyncRun;
}

export interface ImportTraktCollectionRequest {
  library_id?: number;
  library_ids?: number[];
  title: string;
  description?: string;
  preset: "trending" | "popular" | "recommended";
  media_type: "movie" | "tv";
  profile_id?: string;
  limit?: number;
  featured?: boolean;
  poster_url?: string;
  poster_source_url?: string;
  backdrop_source_url?: string;
  sync_schedule?: string;
  management_mode?: LibraryCollectionManagementMode;
  management_source?: string;
  management_key?: string;
}

export interface ImportTraktCollectionResponse {
  collection: LibraryCollection;
  sync_run?: LibraryCollectionSyncRun;
}

// User-facing imports omit library_ids / featured / visibility (server-wide
// concerns). sync_schedule is restricted to a fixed set so we can guarantee
// the >=24h minimum interval without parsing user-supplied cron.
export type UserCollectionSyncSchedule = "" | "daily" | "weekly" | "monthly";

export interface UserImportSharedFields {
  title: string;
  description?: string;
  limit?: number;
  sync_schedule?: UserCollectionSyncSchedule;
  is_shared?: boolean;
  poster_url?: string;
  /** Restrict resolution to these libraries; omitted/empty = entire catalog the user can see. */
  library_ids?: number[];
}

export interface ImportUserMDBListCollectionRequest extends UserImportSharedFields {
  url: string;
}

export interface MDBListListSummary {
  id: number;
  user_id: number;
  user_name: string;
  name: string;
  slug: string;
  description: string;
  mediatype: string;
  items: number;
  likes: number;
  /** Canonical mdblist.com page URL; append /json to fetch list contents. */
  url: string;
}

export interface MDBListDiscoveryResponse {
  /** False when no apikey is set on the server — UI should hide the search box. */
  configured: boolean;
  lists: MDBListListSummary[];
}

export interface ImportUserTMDBCollectionRequest extends UserImportSharedFields {
  preset: ImportTMDBCollectionRequest["preset"];
  media_type: ImportTMDBCollectionRequest["media_type"];
  time_window?: ImportTMDBCollectionRequest["time_window"];
}

export interface ImportUserTraktCollectionRequest extends UserImportSharedFields {
  preset: ImportTraktCollectionRequest["preset"];
  media_type: ImportTraktCollectionRequest["media_type"];
}

// A completed sync always has a non-empty status; the empty-string variant in
// UserCollectionSyncStatus only appears on un-synced rows.
export type UserCollectionSyncResultStatus = Exclude<UserCollectionSyncStatus, "">;

export interface UserCollectionSyncResult {
  status: UserCollectionSyncResultStatus;
  message: string;
  items_matched: number;
  items_unmatched: number;
  started_at: string;
  completed_at: string;
}

export interface ImportUserCollectionResponse {
  collection: Collection;
  sync?: UserCollectionSyncResult;
}

// Admin
export interface AdminUser {
  id: number;
  username: string;
  email: string;
  role: string;
  enabled: boolean;
  library_ids: number[] | null;
  max_playback_quality: string;
  max_streams: number;
  max_transcodes: number;
  max_profiles: number;
  download_allowed: boolean;
  download_transcode_allowed: boolean;
  created_at: string;
  updated_at: string;
  last_active_at?: string;
}

export interface CreateUserRequest {
  username: string;
  email: string;
  password: string;
  role: string;
  create_default_profile?: boolean;
  default_profile_name?: string;
  library_ids?: number[] | null;
  max_playback_quality?: string;
  max_streams?: number;
  max_transcodes?: number;
  max_profiles?: number;
  download_allowed?: boolean;
  download_transcode_allowed?: boolean;
}

export interface UpdateUserRequest {
  username?: string;
  email?: string;
  password?: string;
  role?: string;
  enabled?: boolean;
  library_ids?: number[] | null;
  max_playback_quality?: string;
  max_streams?: number;
  max_transcodes?: number;
  max_profiles?: number;
  download_allowed?: boolean;
  download_transcode_allowed?: boolean;
}

export interface AdminStats {
  total_items: number;
  total_files: number;
  total_users: number;
  total_movies: number;
  total_shows: number;
  active_streams: number;
  total_storage_bytes: number;
  watch_provider_activity: WatchProviderActivity;
}

export interface WatchProviderActivity {
  trakt_connected_profiles: number;
  trakt_enabled_profiles: number;
  trakt_export_enabled: number;
  trakt_scrobble_enabled: number;
  last_sync_completed_at?: string;
  sync_runs_24h: number;
  sync_errors_24h: number;
  imported_watched_24h: number;
  imported_progress_24h: number;
  exported_watched_24h: number;
  pending_exports: number;
  failed_exports: number;
  open_scrobbles: number;
  scrobbles_24h: number;
}

export interface AdminSession {
  session_id: string;
  user_id: number;
  username: string;
  profile_id: string;
  profile_name?: string;
  media_file_id: number;
  requested_media_file_id: number;
  content_id?: string;
  media_title: string;
  media_type: string;
  series_name?: string;
  episode_name?: string;
  season_number?: number | null;
  episode_number?: number | null;
  poster_url?: string;
  play_method: string;
  reporting_node: string;
  node_display_name?: string;
  file_duration: number | null;
  started_at: string;
  updated_at: string;
  is_paused: boolean;
  has_playback_control?: boolean;
  client_ip?: string;
  audio_track_index: number;
  transcode_audio: boolean;
  stream_bitrate_kbps: number | null;
  target_resolution?: string;
  target_video_codec?: string;
  target_audio_codec?: string;
  target_bitrate_kbps: number | null;
  source_container?: string;
  source_bitrate_kbps: number | null;
  source_video_codec?: string;
  source_video_resolution?: string;
  source_audio_codec?: string;
  source_audio_channels: number | null;
  source_audio_language?: string;
  source_audio_title?: string;
  source_audio_layout?: string;
  requested_video_codec?: string;
  requested_video_resolution?: string;
  video_decision?: string;
  audio_decision?: string;
}

export interface OperationalLogEntry {
  id: number;
  timestamp: string;
  level: string;
  component: string;
  message: string;
  request_id?: string;
  user_id?: number | null;
  session_id?: string;
  playback_session_id?: string;
  client_ip?: string;
  node_id?: string;
  attrs?: Record<string, unknown>;
}

export interface AuditLogEntry {
  id: number;
  timestamp: string;
  client_ip: string;
  user_id?: number | null;
  session_id?: string;
  playback_session_id?: string;
  request_id?: string;
  node_id?: string;
  method: string;
  path: string;
  path_pattern?: string;
  status_code: number;
  user_agent?: string;
  duration_ms: number;
}

export interface OperationalLogListResponse {
  entries: OperationalLogEntry[];
  next_cursor?: string;
}

export interface AuditLogListResponse {
  entries: AuditLogEntry[];
  next_cursor?: string;
}

export type AdminLogStream = "app" | "audit";

export interface AdminLogSnapshotMessage {
  type: "snapshot";
  stream: AdminLogStream;
  entries: OperationalLogEntry[] | AuditLogEntry[];
  next_cursor?: string;
}

export interface AdminLogAppendMessage {
  type: "append";
  stream: AdminLogStream;
  entry: OperationalLogEntry | AuditLogEntry;
}

export interface AdminLogErrorMessage {
  type: "error";
  stream: AdminLogStream;
  code: string;
  message: string;
}

export type EventChannel =
  | "catalog"
  | "jobs"
  | "sessions"
  | "tasks"
  | "scans"
  | "history_import"
  | "user_state";

export interface EventsHelloMessage {
  type: "hello";
  schema_version: number;
  connection_id: string;
  available_channels: EventChannel[];
  required_action: "subscribe";
}

export interface EventsSubscribeMessage {
  type: "subscribe";
  request_id?: string;
  channels: EventChannel[];
}

export interface EventsRejectedChannel {
  channel: EventChannel;
  code: string;
  message: string;
}

export interface EventsSubscribedMessage {
  type: "subscribed";
  request_id?: string;
  channels: EventChannel[];
  rejected?: EventsRejectedChannel[];
}

export interface EventsSnapshotMessage<T = unknown> {
  type: "snapshot";
  channel: EventChannel;
  timestamp: string;
  data: T;
}

export interface EventsEventMessage<T = unknown> {
  type: "event";
  channel: EventChannel;
  event: string;
  event_id: string;
  timestamp: string;
  data: T;
}

export interface EventsErrorMessage {
  type: "error";
  code: string;
  message: string;
}

export type EventsStreamMessage =
  | EventsHelloMessage
  | EventsSubscribedMessage
  | EventsSnapshotMessage
  | EventsEventMessage
  | EventsErrorMessage;

export type AdminLogStreamMessage =
  | AdminLogSnapshotMessage
  | AdminLogAppendMessage
  | AdminLogErrorMessage;

export interface AdminPlaybackHistoryItem {
  session_id: string;
  user_id: number;
  username: string;
  profile_id: string;
  profile_name: string;
  media_item_id: string;
  media_file_id: number;
  media_title: string;
  media_type: string;
  play_method: string;
  started_at: string;
  ended_at: string;
  watched_seconds: number;
  duration_seconds: number | null;
  completed: boolean;
}

export interface AdminUserProfile {
  id: string;
  name: string;
}

export interface AdminSettingEntry {
  key: string;
  value: string;
}

export interface AdminDeviceProfileSummary {
  profile_id: string;
  profile_name: string;
  override_count: number;
  last_updated: string;
}

export interface AdminDeviceSummary {
  user_id: number;
  username: string;
  email: string;
  device_id: string;
  device_name: string;
  device_platform: string;
  override_count: number;
  profile_count: number;
  profiles: AdminDeviceProfileSummary[];
  last_updated: string;
}

export interface AdminDeviceDetail {
  user_id: number;
  username: string;
  email: string;
  device_id: string;
  device_name: string;
  device_platform: string;
  last_updated: string;
  settings: {
    user_id: number;
    profile_id: string;
    profile_name?: string;
    device_id: string;
    device_name: string;
    device_platform: string;
    key: string;
    value: string;
    updated_at: string;
  }[];
}

export interface UnmatchedFile {
  id: number;
  media_folder_id: number;
  file_path: string;
  file_size: number;
  container: string;
}

// Libraries
export interface Library {
  id: number;
  paths: string[];
  type: string;
  name: string;
  enabled: boolean;
  metadata_language: string;
  chapter_thumbnails_enabled: boolean;
  chapter_thumbnails_supported: boolean;
  intro_detection_enabled: boolean;
  sort_order: number;
  poster_url?: string;
  last_scanned_at: string | null;
  scan_warning_code?: string | null;
  scan_warning_message?: string | null;
  scan_warning_at?: string | null;
}

export interface LibraryMountCheckRoot {
  path: string;
  reachable: boolean;
  error_code:
    | "not_found"
    | "permission_denied"
    | "not_directory"
    | "stat_failed"
    | "read_failed"
    | null;
  error_message: string | null;
}

export interface LibraryMountCheckResponse {
  status: "ok";
  library_id: number;
  library_name: string;
  healthy: boolean;
  checked_at: string;
  summary: string;
  roots: LibraryMountCheckRoot[];
}

export interface LibrarySkippedRoot {
  library_id: number;
  library_name: string;
  root_path: string;
  reason: string;
  sample_file_path: string;
  file_count: number;
  first_seen_at: string;
  last_seen_at: string;
}

export interface LibraryRootOverride {
  forced_type?: string;
  forced_title?: string;
  forced_year?: number;
  forced_tmdb_id?: string;
  forced_imdb_id?: string;
  forced_tvdb_id?: string;
  note?: string;
}

export interface LibraryRoot {
  library_id: number;
  library_name: string;
  root_path: string;
  state: "resolved" | "ambiguous";
  inferred_type: "movie" | "series" | string;
  type_confidence: "low" | "medium" | "high" | string;
  title: string;
  year: number;
  tmdb_id?: string;
  imdb_id?: string;
  tvdb_id?: string;
  observed_file_count: number;
  sample_file_path?: string;
  evidence_json?: Record<string, unknown>;
  override_source?: string;
  first_seen_at: string;
  last_seen_at: string;
  active_override?: LibraryRootOverride;
}

export interface LibraryRootsResponse {
  items: LibraryRoot[];
  total: number;
}

export interface UpsertLibraryRootOverrideRequest extends LibraryRootOverride {
  library_id: number;
  root_path: string;
}

export interface DeleteLibraryRootOverrideRequest {
  library_id: number;
  root_path: string;
}

export interface StaleMediaID {
  content_id: string;
  library_id: number;
  library_name: string;
  title: string;
  year: number;
  content_type: string;
  provider: string;
  provider_id: string;
  first_seen_at: string;
  last_seen_at: string;
}

export interface CreateLibraryRequest {
  paths: string[];
  type: string;
  name: string;
  enabled?: boolean;
  metadata_language?: string;
  chapter_thumbnails_enabled?: boolean;
  intro_detection_enabled?: boolean;
}

export interface UpdateLibraryRequest extends Partial<CreateLibraryRequest> {}

export interface ScanRequest {
  library_id?: number;
  path?: string;
}

export interface ScanResponse {
  status: "accepted";
  mode: "library" | "subtree" | "file";
  library_id: number;
}

export interface CatalogPathRewrite {
  from: string;
  to: string;
}

export interface CatalogSeedExportRequest {
  library_ids?: number[];
}

export interface CatalogSeedExportResult {
  format_version: number;
  schema_version: number;
  libraries_exported: number;
  items_exported: number;
  cast_exported: number;
  crew_exported: number;
  seasons_exported: number;
  episodes_exported: number;
  files_exported: number;
  library_links_exported: number;
}

export interface CatalogSeedImportRequest {
  source: "local_path" | "export_job" | "bucket_artifact" | "remote_url";
  local_path?: string;
  export_job_id?: string;
  artifact_key?: string;
  remote_url?: string;
  conflict_mode: "skip_existing" | "overwrite_existing";
  path_rewrites: CatalogPathRewrite[];
}

export interface CatalogSeedImportSource {
  key: string;
  size_bytes: number;
  last_modified?: string;
}

export interface CatalogSeedImportSourcesResponse {
  sources: CatalogSeedImportSource[];
}

export interface CatalogSeedImportResponse {
  libraries_created: number;
  libraries_matched: number;
  items_created: number;
  items_updated: number;
  seasons_created: number;
  seasons_updated: number;
  episodes_created: number;
  episodes_updated: number;
  files_created: number;
  files_updated: number;
  links_created: number;
  credits_replaced: number;
  skipped: number;
  unmatched_roots?: string[];
}

export type AdminJobStatus = "queued" | "running" | "completed" | "failed";

export interface LibraryRefreshJobRequest {
  library_id: number;
  library_name?: string;
}

export interface LibraryRefreshJobResult {
  library_id: number;
  library_name?: string;
  total_items: number;
  items_with_ids: number;
  items_without_ids: number;
  refreshed_ok: number;
  refreshed_failed: number;
  pipeline_ok: number;
  pipeline_failed: number;
}

export interface AdminJob {
  id: string;
  job_type: string;
  status: AdminJobStatus;
  created_by_user_id: number;
  request_payload: CatalogSeedExportRequest | LibraryRefreshJobRequest | Record<string, unknown>;
  result_payload: CatalogSeedExportResult | LibraryRefreshJobResult | Record<string, unknown>;
  message: string;
  error_message?: string;
  progress_current: number;
  progress_total: number;
  artifact_size_bytes: number;
  public_url?: string;
  requested_at: string;
  started_at?: string;
  completed_at?: string;
  heartbeat_at?: string;
  expires_at?: string;
  published_at?: string;
  download_url?: string;
  download_expires_at?: string;
}

export interface AdminJobsResponse {
  jobs: AdminJob[];
}

// Library Provider Chain
export interface LibraryProviderChainEntry {
  plugin_installation_id: number;
  capability_id: string;
  provider_slug: string;
  priority: number;
  enabled: boolean;
}

export interface LibraryProviderChainResponse {
  levels: Record<string, LibraryProviderChainEntry[]>;
}

export interface SetLibraryChainRequest {
  levels: Record<
    string,
    Array<{
      plugin_installation_id: number;
      capability_id: string;
      priority: number;
      enabled: boolean;
    }>
  >;
}

export interface PluginConfigSchema {
  key: string;
  title: string;
  description?: string;
  json_schema: string;
  required: boolean;
  admin_form?: PluginAdminForm;
}

export interface ConnectionCheckResponse {
  success: boolean;
  message: string;
}

export interface AdminSettingsConnectionCheckRequest {
  values: Record<string, string>;
  dirty_keys: string[];
}

export interface PluginAdminForm {
  fields: PluginAdminFormField[];
  submit_label?: string;
}

export interface PluginAdminFormFieldOption {
  value: string;
  label: string;
  description?: string;
}

export interface PluginAdminFormField {
  key: string;
  label: string;
  description?: string;
  control: "TEXT" | "TEXTAREA" | "PASSWORD" | "NUMBER" | "SWITCH" | "SELECT";
  placeholder?: string;
  required: boolean;
  secret: boolean;
  multiline: boolean;
  default_value?: unknown;
  options?: PluginAdminFormFieldOption[];
  rows?: number;
}

export interface PluginCapability {
  type: string;
  id: string;
  display_name: string;
  description?: string;
  subscriptions?: string[];
  config_schema?: PluginConfigSchema[];
  metadata?: Record<string, unknown>;
}

export interface PluginRoute {
  id: string;
  method: string;
  path: string;
  access: string;
  navigable: boolean;
  navigation_label: string;
  navigation_kind: string;
  static_asset: boolean;
}

export interface PluginAsset {
  path: string;
  content_type: string;
  integrity?: string;
}

export interface PluginConfigValue {
  key: string;
  value: Record<string, unknown>;
}

export interface PluginAuthBinding {
  capability_id: string;
  enabled: boolean;
  display_order: number;
  auto_provision: boolean;
  default_login: boolean;
  created_at: string;
  updated_at: string;
}

export interface PluginTaskBinding {
  capability_id: string;
  enabled: boolean;
  trigger: Record<string, unknown>;
  created_at: string;
  updated_at: string;
}

export interface PluginRepository {
  id: number;
  url: string;
  display_name: string;
  enabled: boolean;
  last_fetched_at?: string | null;
  created_at?: string;
  updated_at?: string;
}

export interface PluginCatalogEntry {
  repository_id: number;
  plugin_id: string;
  version: string;
  archive_url: string;
  capabilities: PluginCapability[];
  global_config_schema: PluginConfigSchema[];
  user_config_schema: PluginConfigSchema[];
  routes: PluginRoute[];
  assets: PluginAsset[];
  metadata?: Record<string, unknown>;
}

export interface PluginInstallation {
  id: number;
  repository_id?: number | null;
  plugin_id: string;
  version: string;
  install_path: string;
  enabled: boolean;
  capabilities: PluginCapability[];
  global_config_schema: PluginConfigSchema[];
  user_config_schema: PluginConfigSchema[];
  routes: PluginRoute[];
  assets: PluginAsset[];
  metadata?: Record<string, unknown>;
  global_configs: PluginConfigValue[];
  auth_bindings: PluginAuthBinding[];
  task_bindings: PluginTaskBinding[];
  update_policy: string;
  available_version?: string | null;
  legacy_metadata_import_types?: string[];
  created_at?: string;
  updated_at?: string;
}

export interface CreatePluginRepositoryRequest {
  url: string;
  display_name: string;
  enabled?: boolean;
}

export interface UpdatePluginRepositoryRequest {
  url?: string;
  display_name?: string;
  enabled?: boolean;
}

export interface InstallPluginRequest {
  repository_id?: number;
  plugin_id?: string;
  version?: string;
  archive_url?: string;
}

export interface UpdatePluginInstallationRequest {
  enabled?: boolean;
  update_policy?: string;
}

export interface SavePluginConfigRequest {
  key: string;
  value: Record<string, unknown>;
}

export interface SavePluginAuthBindingRequest {
  capability_id: string;
  enabled: boolean;
  display_order: number;
  auto_provision: boolean;
  default_login: boolean;
}

export interface SavePluginTaskBindingRequest {
  enabled: boolean;
  trigger: Record<string, unknown>;
}

export interface PluginTaskBindingUpdateResponse {
  restart_required: boolean;
}

export interface PluginSettingsSummary {
  id: number;
  plugin_id: string;
  version: string;
  user_config_schema: PluginConfigSchema[];
  routes: PluginRoute[];
  assets: PluginAsset[];
}

export interface PluginSettingsListResponse {
  installations: PluginSettingsSummary[];
}

export interface PluginSettingsDetailResponse {
  installation: PluginSettingsSummary;
  values: Record<string, string>;
}

export interface UpdatePluginSettingsRequest {
  values: Record<string, string>;
}

// Stream Nodes
export interface StreamNode {
  id: number;
  name: string;
  type: string;
  url: string;
  enabled: boolean;
  healthy: boolean;
  active_jobs: number;
  last_health_check: string | null;
  created_at: string;
}

export interface CreateNodeRequest {
  name: string;
  type: string;
  url: string;
}

export interface UpdateNodeRequest {
  name?: string;
  url?: string;
  enabled?: boolean;
}

export interface CheckNodeResponse {
  healthy: boolean;
  active_jobs: number;
}

// User-facing library (simplified, no admin fields)
export interface UserLibrary {
  id: number;
  name: string;
  type: string;
  sort_order: number;
  poster_url?: string;
}

export interface LibraryPlaybackPreference {
  profile_id: string;
  library_id: number;
  audio_language?: string;
  subtitle_language?: string;
  subtitle_mode?: string;
  show_forced_subtitles?: boolean;
  updated_at?: string;
}

// Progress entry from GET /progress
export interface ProgressEntry {
  media_item_id: string;
  position_seconds: number;
  duration_seconds: number;
  completed: boolean;
  updated_at: string;
}

export interface ProgressListResponse {
  progress: ProgressEntry[];
}

// Sections
export interface SectionItemUpcomingEvent {
  type: "movie" | "episode" | "season_premiere";
  air_date: string;
  air_time?: string;
  episode_title?: string | null;
  season_number?: number | null;
  episode_number?: number | null;
  badges: string[];
}

export interface SectionItem {
  content_id: string;
  type: "movie" | "series" | "season" | "episode";
  title: string;
  series_id?: string;
  series_title?: string;
  season_number?: number | null;
  episode_number?: number | null;
  year: number;
  runtime?: number;
  genres: string[];
  studios?: string[];
  networks?: string[];
  content_rating?: string;
  status: "pending" | "matched" | "unmatched" | "ambiguous";
  show_status?: string;
  rating_imdb: number | null;
  rating_tmdb?: number | null;
  rating_rt_critic?: number | null;
  rating_rt_audience?: number | null;
  original_language?: string;
  overview: string;
  item_source?: string;
  position_seconds?: number;
  duration_seconds?: number;
  progress_updated_at?: string;
  poster_url: string;
  poster_thumbhash: string;
  backdrop_url: string;
  backdrop_thumbhash: string;
  logo_url: string;
  overlay_summary?: OverlaySummary | null;
  badges?: string[];
  user_state?: MediaItemUserState;
  upcoming_event?: SectionItemUpcomingEvent | null;
}

export interface ResolvedSection {
  id: string;
  section_type: string;
  title: string;
  featured: boolean;
  item_limit: number;
  total_count: number;
  is_custom: boolean;
  customized: boolean;
  items: SectionItem[];
}

export interface SectionsResponse {
  sections: ResolvedSection[];
}

export interface DiscoverRow {
  type: string;
  label: string;
  /** URL kind for the dedicated "see all" page (e.g. "for-you-main", "cluster", "genre"). */
  section_kind?: string;
  /** URL key paired with section_kind when needed (cluster index or genre name). */
  section_key?: string;
  items: SectionItem[];
}

export interface DiscoverResponse {
  rows: DiscoverRow[];
}

export interface RecommendationSectionResponse {
  kind: string;
  key?: string;
  type: string;
  label: string;
  items: SectionItem[];
}

export interface ResolvedSectionLayout {
  id: string;
  section_type: string;
  title: string;
  featured: boolean;
  item_limit: number;
  is_custom: boolean;
  customized: boolean;
}

export interface HomeLayoutResponse {
  sections: ResolvedSectionLayout[];
}

export interface LibraryLayoutResponse {
  sections: ResolvedSectionLayout[];
}

export interface HomeSectionItemsResponse {
  section: ResolvedSection;
}

export interface CollectionSectionConfig {
  library_collection_id: string;
}

export function isFilterSectionConfig(
  config: Record<string, unknown>,
): config is FilterConfig & Record<string, unknown> {
  return config != null && "match" in config && "groups" in config;
}

export interface PageSectionConfig {
  id: string;
  scope: string;
  library_id: number | null;
  position: number;
  section_type: string;
  title: string;
  featured: boolean;
  item_limit: number;
  config: Record<string, unknown>;
  enabled: boolean;
  created_at: string;
  updated_at: string;
}

export interface PageSectionListResponse {
  sections: PageSectionConfig[];
}

export interface FilterRule {
  field: string;
  op: string;
  value: string | number | boolean | [string | number, string | number];
}

export interface FilterGroup {
  match: "all" | "any";
  rules: FilterRule[];
}

export interface FilterConfig {
  match: "all" | "any";
  groups: FilterGroup[];
  sort?: string;
  order?: string;
}

export function createEmptyQueryDefinition(): QueryDefinition {
  return {
    library_ids: [],
    match: "all",
    groups: [],
    sort: { field: "added_at", order: "desc" },
  };
}

export function normalizeQueryDefinition(value?: QueryDefinitionInput | null): QueryDefinition {
  const normalizeField = (field?: string) => normalizeQuerySortField(field) ?? field;

  return {
    library_ids: [...(value?.library_ids ?? [])],
    media_scope:
      value?.media_scope === "movie" ||
      value?.media_scope === "series" ||
      value?.media_scope === "episode"
        ? value.media_scope
        : undefined,
    match: value?.match === "any" ? "any" : "all",
    groups: (value?.groups ?? []).map((group) => ({
      match: group.match === "any" ? "any" : "all",
      rules: group.rules.map((rule) => ({
        ...rule,
        field: normalizeField(rule.field) ?? rule.field,
      })),
    })),
    sort: {
      field: normalizeQuerySortField(value?.sort?.field) ?? "added_at",
      order:
        value?.sort?.order === "asc" || value?.sort?.order === "desc"
          ? value.sort.order
          : getDefaultQuerySortOrder(value?.sort?.field),
    },
    limit: value?.limit,
  };
}

export function queryDefinitionFromSectionConfig(
  config?: Record<string, unknown>,
): QueryDefinition {
  if (!config) {
    return createEmptyQueryDefinition();
  }

  const libraryIds: number[] = [];
  if (Array.isArray(config.library_ids)) {
    for (const value of config.library_ids) {
      if (typeof value === "number" && Number.isInteger(value) && !libraryIds.includes(value)) {
        libraryIds.push(value);
      }
    }
  }
  if (Array.isArray(config.filter_library_ids)) {
    for (const value of config.filter_library_ids) {
      if (typeof value === "number" && Number.isInteger(value) && !libraryIds.includes(value)) {
        libraryIds.push(value);
      }
    }
  }
  if (
    typeof config.filter_library_id === "number" &&
    Number.isInteger(config.filter_library_id) &&
    !libraryIds.includes(config.filter_library_id)
  ) {
    libraryIds.push(config.filter_library_id);
  }

  const maybeGroups = Array.isArray(config.groups) ? (config.groups as QueryGroup[]) : [];
  const mediaScope =
    config.media_scope === "movie" || config.filter_type === "movie"
      ? "movie"
      : config.media_scope === "series" || config.filter_type === "series"
        ? "series"
        : config.media_scope === "episode" || config.filter_type === "episode"
          ? "episode"
          : undefined;

  const legacySortField = typeof config.sort === "string" ? config.sort : undefined;
  const legacySortOrder = typeof config.order === "string" ? config.order : undefined;

  return normalizeQueryDefinition({
    library_ids: libraryIds,
    media_scope: mediaScope,
    match: config.match === "any" ? "any" : "all",
    groups: maybeGroups,
    sort:
      config.sort && typeof config.sort === "object"
        ? (config.sort as QuerySort)
        : {
            field: (legacySortField as QuerySort["field"] | undefined) ?? "added_at",
            order: (legacySortOrder as QuerySort["order"] | undefined) ?? "desc",
          },
  });
}

export function queryDefinitionToSectionConfig(query: QueryDefinition): Record<string, unknown> {
  const normalized = normalizeQueryDefinition(query);
  return {
    library_ids: normalized.library_ids,
    media_scope: normalized.media_scope,
    match: normalized.match,
    groups: normalized.groups,
    sort: normalized.sort,
  };
}

export interface SectionOverride {
  id?: string;
  section_id?: string;
  position?: number;
  hidden?: boolean;
  section_type?: string;
  title?: string;
  featured?: boolean;
  item_limit?: number;
  config?: Record<string, unknown>;
  removed?: boolean;
}

export interface SaveOverridesRequest {
  scope: string;
  library_id?: string;
  overrides: SectionOverride[];
}

export interface ProfileSectionOverridesResponse {
  overrides: SectionOverride[];
}

export interface SettingsSectionEntry {
  id: string;
  section_type: string;
  title: string;
  featured: boolean;
  item_limit: number;
  hidden: boolean;
  is_custom: boolean;
  customized: boolean;
  position: number;
  config?: Record<string, unknown>;
}

export interface SettingsSectionsResponse {
  sections: SettingsSectionEntry[];
}

// Sidebar Pins
export interface SidebarPin {
  type: "section" | "collection";
  id: string;
  label: string;
}

export type SidebarPins = Record<string, SidebarPin[]>;

// Signup
export interface SignupRequest {
  username: string;
  email: string;
  password: string;
  invite_code: string;
}

export interface SignupStatusResponse {
  enabled: boolean;
}

// Invite Codes
export interface InviteCode {
  id: number;
  code: string;
  label: string;
  max_uses: number;
  use_count: number;
  created_by: number;
  enabled: boolean;
  created_at: string;
  updated_at: string;
}

export interface CreateInviteCodeRequest {
  code?: string;
  label: string;
  max_uses: number;
}

export interface UpdateInviteCodeRequest {
  label?: string;
  max_uses?: number;
  enabled?: boolean;
}

export interface TopUpInviteCodeRequest {
  additional_uses: number;
}

// API Keys
export interface AdminAPIKey {
  id: number;
  user_id: number;
  username: string;
  label: string;
  key: string;
  rate_tier: string;
  created_at: string;
  last_used_at?: string;
}

export interface AdminCreateAPIKeyRequest {
  label: string;
  user_id?: number;
}

// Rate Limiting
export interface RateLimitTierConfig {
  requests_per_second: number;
  requests_per_minute: number;
  burst: number;
}

export interface RateLimitAuthEndpointConfig {
  requests_per_minute: number;
  burst: number;
}

export interface RateLimitConfig {
  enabled: boolean;
  backend: string;
  global_requests_per_second: number;
  tiers: Record<string, RateLimitTierConfig>;
  ip_requests_per_second: number;
  ip_requests_per_minute: number;
  ip_burst: number;
  auth_endpoints: Record<string, RateLimitAuthEndpointConfig>;
}

// IP visibility
export interface UserIPEntry {
  client_ip: string;
  first_seen: string;
  last_seen: string;
  request_count: number;
}

export interface IPUserEntry {
  user_id: number;
  username: string;
  first_seen: string;
  last_seen: string;
  request_count: number;
}

// API Error
export interface ApiError {
  error: string;
  message: string;
  retry_after_seconds?: number;
  unmatched_roots?: string[];
  active_job_id?: string;
  active_job?: AdminJob;
}

// Subtitle search types
export interface SubtitleSearchRequest {
  media_file_id: number;
  languages: string[];
}

export interface SubtitleResult {
  id: string;
  provider: string;
  language: string;
  release_name: string;
  format: string;
  score: number;
  downloads: number;
  hearing_impaired: boolean;
  upload_date?: string;
}

export interface SubtitleSearchResponse {
  results: SubtitleResult[];
  warnings?: string[];
}

export interface SubtitleDownloadRequest {
  media_file_id: number;
  provider: string;
  subtitle_id: string;
  language: string;
  release_name: string;
  format: string;
  score: number;
  hearing_impaired: boolean;
}

export interface DownloadedSubtitle {
  id: number;
  media_file_id: number;
  provider: string;
  language: string;
  format: string;
  release_name: string;
  score: number;
  hearing_impaired: boolean;
  created_at: string;
}

export interface SubtitleProviderConfig {
  provider_name: string;
  enabled: boolean;
  has_api_key: boolean;
  has_credentials: boolean;
  updated_at: string;
}

export interface SubtitleProviderUpdateRequest {
  enabled: boolean;
  api_key?: string;
  username?: string;
  password?: string;
}

export interface SubtitleProviderTestRequest {
  enabled?: boolean;
  api_key?: string;
  username?: string;
  password?: string;
}

export interface SubtitleProviderTestResponse {
  success: boolean;
  error?: string;
}

// --- Task Framework ---

export type TaskState = "idle" | "running" | "cancelling";

export type TaskCategory = "library" | "metadata" | "system";

export type TriggerType = "interval" | "daily" | "weekly" | "startup";

export interface TriggerConfig {
  type: TriggerType;
  interval_ms?: number;
  time_of_day?: string;
  day_of_week?: number;
  max_runtime_ms?: number;
}

export interface ExecutionResult {
  id: number;
  task_key: string;
  started_at: string;
  completed_at: string;
  status: "completed" | "failed" | "cancelled";
  error_message?: string;
  result_data?: Record<string, unknown>;
  duration_ms: number;
}

export interface TaskInfo {
  key: string;
  name: string;
  description: string;
  category: TaskCategory;
  state: TaskState;
  progress: number;
  progress_message?: string;
  last_execution?: ExecutionResult;
  triggers: TriggerConfig[];
  next_run_at?: string;
}

// Match dialog types
export interface MatchCandidate {
  title: string;
  year: number;
  content_type: string;
  provider_ids: Record<string, string>;
  image_url: string;
  overview: string;
  sources: string[];
  agreement_hints: string[];
}

export interface ItemMatchSearchRequest {
  title?: string;
  year?: number;
  imdb_id?: string;
  tmdb_id?: string;
  tvdb_id?: string;
}

export interface ItemMatchSearchResponse {
  candidates: MatchCandidate[];
}

export interface ItemMatchApplyRequest {
  provider_ids: Record<string, string>;
}

// Image selector types
export interface RemoteImage {
  provider_id: string;
  url: string;
  original_url: string;
  type: "poster" | "backdrop" | "logo" | "still";
  language: string;
  width: number;
  height: number;
  rating: number;
}

export interface CurrentImages {
  poster_url?: string;
  backdrop_url?: string;
  logo_url?: string;
}

export interface ItemImagesResponse {
  images: RemoteImage[];
  current: CurrentImages;
  provider_errors?: Record<string, string>;
}

export interface ApplyItemImageRequest {
  original_url: string;
  type: string;
  provider_id: string;
}

export interface ApplyItemImageResponse {
  content_id: string;
  stored_path: string;
  thumbhash: string;
}

export interface UnmatchedLibraryItem {
  content_id: string;
  title: string;
  year: number;
  content_type: string;
  library_id: number;
  library_name: string;
  status: string;
}

export interface UnmatchedLibraryItemsResponse {
  items: UnmatchedLibraryItem[];
  total: number;
}

export interface FilesystemBrowseEntry {
  name: string;
  path: string;
}

export interface FilesystemBrowseResponse {
  path: string;
  parent: string;
  entries: FilesystemBrowseEntry[];
}
