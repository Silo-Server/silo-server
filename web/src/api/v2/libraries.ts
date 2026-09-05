import type {
  AdminJob,
  CreateLibraryRequest,
  Library,
  LibraryMetadataMatchQueueStatus,
  LibraryMountCheckResponse,
  LibraryMountCheckRoot,
  LibraryProviderChainResponse,
  LibraryRoot,
  LibrarySkippedRoot,
  StaleMediaID,
  UnmatchedLibraryItem,
} from "@/api/types";
import type { V2Body, V2Result } from "@/api/v2/request";
import type { components } from "@/api/v2/schema";

/**
 * Library management as the admin screens still model it. v2 renders every
 * id as a string; the screens key rows, dialogs, and query caches by numeric
 * library id, so the adapters below convert at the boundary and keep the
 * hook return shapes stable.
 */

type LibraryV2 = components["schemas"]["Library"];
type LibraryRootV2 = components["schemas"]["LibraryRoot"];
type SkippedRootV2 = components["schemas"]["SkippedRoot"];
type StaleMediaIDV2 = components["schemas"]["StaleMediaID"];
type UnmatchedItemV2 = components["schemas"]["UnmatchedItem"];
type LibraryMountCheckV2 = components["schemas"]["LibraryMountCheck"];
type MetadataMatchQueueStatusV2 = components["schemas"]["MetadataMatchQueueStatus"];
type ProviderChainLevelV2 = components["schemas"]["ProviderChainLevel"];
type ProviderChainLevelInputV2 = components["schemas"]["ProviderChainLevelInput"];
type AdminJobV2 = components["schemas"]["AdminJob"];

export function libraryFromV2(library: LibraryV2): Library {
  return {
    id: Number(library.id),
    paths: library.paths,
    type: library.type,
    name: library.name,
    enabled: library.enabled,
    metadata_language: library.metadata_language,
    auto_translate_metadata: library.auto_translate_metadata,
    chapter_thumbnails_enabled: library.chapter_thumbnails_enabled,
    chapter_thumbnails_supported: library.chapter_thumbnails_supported,
    intro_detection_enabled: library.intro_detection_enabled,
    trailer_kinds: library.trailer_kinds,
    sort_order: library.sort_order,
    poster_url: library.poster_url,
    last_scanned_at: library.last_scanned_at ?? null,
    scan_warning_code: library.scan_warning_code,
    scan_warning_message: library.scan_warning_message,
    scan_warning_at: library.scan_warning_at,
  };
}

/**
 * The create body the form builds also carries `enabled` and
 * `auto_translate_metadata`; v1 ignored them on create and v2 rejects unknown
 * members, so only the members createLibrary declares are sent.
 */
export function libraryCreateToV2(body: CreateLibraryRequest): V2Body<"POST /api/v2/libraries"> {
  return {
    paths: body.paths,
    type: body.type,
    name: body.name,
    ...(body.metadata_language === undefined ? {} : { metadata_language: body.metadata_language }),
    ...(body.chapter_thumbnails_enabled === undefined
      ? {}
      : { chapter_thumbnails_enabled: body.chapter_thumbnails_enabled }),
    ...(body.intro_detection_enabled === undefined
      ? {}
      : { intro_detection_enabled: body.intro_detection_enabled }),
    ...(body.trailer_kinds === undefined ? {} : { trailer_kinds: body.trailer_kinds }),
  };
}

function recordOrUndefined(value: unknown): Record<string, unknown> | undefined {
  if (typeof value !== "object" || value === null || Array.isArray(value)) return undefined;
  return value as Record<string, unknown>;
}

export function libraryRootFromV2(root: LibraryRootV2): LibraryRoot {
  return {
    library_id: Number(root.library_id),
    library_name: root.library_name,
    root_path: root.root_path,
    state: root.state as LibraryRoot["state"],
    inferred_type: root.inferred_type,
    type_confidence: root.type_confidence,
    title: root.title,
    year: root.year,
    tmdb_id: root.tmdb_id,
    imdb_id: root.imdb_id,
    tvdb_id: root.tvdb_id,
    observed_file_count: root.observed_file_count,
    sample_file_path: root.sample_file_path,
    evidence_json: recordOrUndefined(root.evidence_json),
    override_source: root.override_source,
    first_seen_at: root.first_seen_at,
    last_seen_at: root.last_seen_at,
    active_override: root.active_override,
    content_id: root.content_id,
  };
}

export function skippedRootFromV2(root: SkippedRootV2): LibrarySkippedRoot {
  return { ...root, library_id: Number(root.library_id) };
}

export function staleMediaIDFromV2(row: StaleMediaIDV2): StaleMediaID {
  return { ...row, library_id: Number(row.library_id) };
}

export function unmatchedItemFromV2(item: UnmatchedItemV2): UnmatchedLibraryItem {
  return { ...item, library_id: Number(item.library_id) };
}

export function mountCheckFromV2(result: LibraryMountCheckV2): LibraryMountCheckResponse {
  return {
    ...result,
    status: result.status as LibraryMountCheckResponse["status"],
    library_id: Number(result.library_id),
    roots: result.roots.map((root) => ({
      ...root,
      error_code: root.error_code as LibraryMountCheckRoot["error_code"],
    })),
  };
}

export function metadataMatchQueueStatusFromV2(
  queue: MetadataMatchQueueStatusV2,
): LibraryMetadataMatchQueueStatus {
  return { ...queue, library_id: Number(queue.library_id) };
}

/** Converts the v2 provider-chain level list into the per-level map the library form edits. */
export function providerChainFromV2(levels: ProviderChainLevelV2[]): LibraryProviderChainResponse {
  const mapped: LibraryProviderChainResponse["levels"] = {};
  for (const level of levels) {
    mapped[level.content_level] = level.entries.map((entry) => ({
      plugin_installation_id: Number(entry.plugin_installation_id),
      capability_id: entry.capability_id,
      provider_slug: entry.provider_slug,
      priority: entry.priority,
      enabled: entry.enabled,
    }));
  }
  return { levels: mapped };
}

/** Converts the library form's per-level map into the v2 setLibraryProviders body. */
export function providerChainToV2(
  levels: Record<
    string,
    Array<{
      plugin_installation_id: number;
      capability_id: string;
      priority: number;
      enabled: boolean;
    }>
  >,
): V2Body<"PUT /api/v2/libraries/{id}/providers"> {
  const mapped: ProviderChainLevelInputV2[] = Object.entries(levels).map(
    ([content_level, entries]) => ({
      content_level,
      entries: entries.map((entry) => ({
        plugin_installation_id: String(entry.plugin_installation_id),
        capability_id: entry.capability_id,
        priority: entry.priority,
        enabled: entry.enabled,
      })),
    }),
  );
  return { levels: mapped };
}

export function adminJobFromV2(job: AdminJobV2): AdminJob {
  return {
    id: job.id,
    job_type: job.job_type,
    status: job.status as AdminJob["status"],
    created_by_user_id: Number(job.created_by_user_id),
    request_payload: recordOrUndefined(job.request_payload) ?? {},
    result_payload: recordOrUndefined(job.result_payload) ?? {},
    message: job.message,
    error_message: job.error_message,
    progress_current: job.progress_current,
    progress_total: job.progress_total,
    artifact_size_bytes: job.artifact_size_bytes,
    public_url: job.public_url,
    requested_at: job.requested_at,
    started_at: job.started_at,
    completed_at: job.completed_at,
    heartbeat_at: job.heartbeat_at,
    expires_at: job.expires_at,
    published_at: job.published_at,
  };
}

/** The library listing as the admin screens consume it: every library, numeric ids. */
export function librariesFromV2(page: V2Result<"GET /api/v2/libraries">): Library[] {
  return page.items.map(libraryFromV2);
}
