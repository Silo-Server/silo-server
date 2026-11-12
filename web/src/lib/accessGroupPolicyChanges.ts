import type { AccessGroup } from "@/api/types";
import { t } from "@/i18n";
import { PERMISSION_MARKER_EDIT, PERMISSION_METADATA_CURATION } from "@/lib/permissions";
import { PLAYBACK_QUALITY_OPTIONS, playbackQualityPresetFromValue } from "@/lib/playback-quality";
import { formatStreamBitrateLimit } from "@/lib/streamBitrateLimit";
import {
  formatRequestApproval,
  formatRequestQuota,
  resolveRequestTerms,
  type RequestLimitLayer,
  type RequestServerDefaults,
} from "@/lib/requestAccess";

/** The group settings members inherit, which is what a move compares. */
export type GroupPolicy = Pick<
  AccessGroup,
  | "library_ids"
  | "max_playback_quality"
  | "download_allowed"
  | "download_transcode_allowed"
  | "transcode_allowed"
  | "audio_transcode_allowed"
  | "max_streams"
  | "max_transcodes"
  | "max_remote_stream_bitrate_kbps"
  | "max_local_stream_bitrate_kbps"
  | "max_profiles"
  | "allowed_permissions"
  | "requests_allowed"
>;

// Mirrors access.NoGroupPolicy(): what an account in no group inherits, so a
// move out of no group lists what it loses. Keep in sync with
// internal/access/groups.go.
export const NO_GROUP_POLICY: GroupPolicy = {
  library_ids: null,
  max_playback_quality: "",
  download_allowed: true,
  download_transcode_allowed: false,
  transcode_allowed: true,
  audio_transcode_allowed: true,
  max_streams: 0,
  max_transcodes: 0,
  max_remote_stream_bitrate_kbps: 0,
  max_local_stream_bitrate_kbps: 0,
  // access.DefaultMaxProfiles: profiles were never unlimited.
  max_profiles: 5,
  allowed_permissions: null,
  requests_allowed: true,
};

export interface PolicyChange {
  label: string;
  from: string;
  to: string;
}

function limit(value: number) {
  return value > 0 ? String(value) : t("web.admin.access.groups.changes.unlimited");
}

// Whole-kbps precision: 128 and 64 kbps must not both read as 0.1 Mbps.
function bitrate(kbps: number) {
  return kbps <= 0
    ? t("web.admin.access.groups.changes.unlimited")
    : formatStreamBitrateLimit(kbps);
}

function allowed(value: boolean) {
  return value
    ? t("web.admin.access.groups.changes.allowed")
    : t("web.admin.access.groups.changes.notAllowed");
}

function libraries(ids: number[] | null, names: ReadonlyMap<number, string>) {
  if (ids === null) return t("web.admin.access.groups.card.allLibraries");
  if (ids.length === 0) return t("web.admin.access.groups.changes.noLibraries");
  return ids
    .map(
      (id) => names.get(id) ?? t("web.admin.access.groups.changes.libraryId", { id: String(id) }),
    )
    .join(", ");
}

// Names the group editor shows for the assignable permissions.
function permissionLabel(value: string) {
  if (value === PERMISSION_METADATA_CURATION)
    return t("web.admin.access.groups.permissions.metadataCuration");
  if (value === PERMISSION_MARKER_EDIT) return t("web.admin.access.groups.permissions.markerEdit");
  return value;
}

function permissions(values: string[] | null) {
  if (values === null) return t("web.admin.access.groups.changes.allPermissions");
  if (values.length === 0) return t("web.admin.access.groups.changes.noPermissions");
  return values.map(permissionLabel).sort().join(", ");
}

function quality(value: string) {
  const preset = playbackQualityPresetFromValue(value);
  return PLAYBACK_QUALITY_OPTIONS.find((option) => option.value === preset)?.label ?? value;
}

function sameIds(a: number[] | null, b: number[] | null) {
  if (a === null || b === null) return a === b;
  if (a.length !== b.length) return false;
  const set = new Set(a);
  return b.every((id) => set.has(id));
}

/**
 * How the request limit and approval that members inherit change between two
 * groups' request settings (null for no group), each over the server-wide
 * defaults. The group switch itself is groupPolicyChanges' "Requests".
 */
export function requestTermsChanges(
  from: RequestLimitLayer | null,
  to: RequestLimitLayer | null,
  server: RequestServerDefaults,
): PolicyChange[] {
  const terms = (layer: RequestLimitLayer | null) =>
    resolveRequestTerms(layer ? [[{ kind: "group", name: "" }, layer]] : [], server);
  const a = terms(from);
  const b = terms(to);
  const changes: PolicyChange[] = [];
  const quotaFrom = formatRequestQuota(a.quota);
  const quotaTo = formatRequestQuota(b.quota);
  if (quotaFrom !== quotaTo)
    changes.push({
      label: t("web.admin.access.groups.changes.requestLimit"),
      from: quotaFrom,
      to: quotaTo,
    });
  if (a.autoApprove !== b.autoApprove) {
    changes.push({
      label: t("web.admin.access.groups.changes.requestApproval"),
      from: formatRequestApproval(a.autoApprove),
      to: formatRequestApproval(b.autoApprove),
    });
  }
  return changes;
}

/** The group policies that differ between two groups, as members inheriting them see it. */
export function groupPolicyChanges(
  from: GroupPolicy,
  to: GroupPolicy,
  libraryNames: ReadonlyMap<number, string> = new Map(),
): PolicyChange[] {
  const changes: PolicyChange[] = [];
  const add = (label: string, a: string, b: string) => {
    if (a !== b) changes.push({ label, from: a, to: b });
  };
  if (!sameIds(from.library_ids, to.library_ids)) {
    changes.push({
      label: t("web.admin.access.groups.changes.libraries"),
      from: libraries(from.library_ids, libraryNames),
      to: libraries(to.library_ids, libraryNames),
    });
  }
  add(
    t("web.admin.access.groups.changes.playbackQuality"),
    quality(from.max_playback_quality),
    quality(to.max_playback_quality),
  );
  add(
    t("web.admin.access.groups.changes.downloads"),
    allowed(from.download_allowed),
    allowed(to.download_allowed),
  );
  add(
    t("web.admin.access.groups.changes.convertedDownloads"),
    allowed(from.download_transcode_allowed),
    allowed(to.download_transcode_allowed),
  );
  add(
    t("web.admin.access.groups.changes.videoTranscoding"),
    allowed(from.transcode_allowed),
    allowed(to.transcode_allowed),
  );
  add(
    t("web.admin.access.groups.changes.audioTranscoding"),
    allowed(from.audio_transcode_allowed),
    allowed(to.audio_transcode_allowed),
  );
  add(
    t("web.admin.access.groups.changes.concurrentStreams"),
    limit(from.max_streams),
    limit(to.max_streams),
  );
  add(
    t("web.admin.access.groups.changes.concurrentTranscodes"),
    limit(from.max_transcodes),
    limit(to.max_transcodes),
  );
  add(
    t("web.admin.access.groups.changes.householdProfiles"),
    String(from.max_profiles),
    String(to.max_profiles),
  );
  add(
    t("web.admin.access.groups.changes.remoteBitrate"),
    bitrate(from.max_remote_stream_bitrate_kbps),
    bitrate(to.max_remote_stream_bitrate_kbps),
  );
  add(
    t("web.admin.access.groups.changes.localBitrate"),
    bitrate(from.max_local_stream_bitrate_kbps),
    bitrate(to.max_local_stream_bitrate_kbps),
  );
  add(
    t("web.admin.access.groups.changes.permissions"),
    permissions(from.allowed_permissions),
    permissions(to.allowed_permissions),
  );
  add(
    t("web.admin.access.groups.changes.requests"),
    allowed(from.requests_allowed),
    allowed(to.requests_allowed),
  );
  return changes;
}
