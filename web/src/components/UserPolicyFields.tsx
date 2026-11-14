import { useId, useState, type ReactNode } from "react";

import type {
  AccessGroup,
  AdminPolicyDefaults,
  AdminUser,
  AdminUserEffectivePolicy,
  Library,
} from "@/api/types";
import { DownloadsServerNotice } from "@/components/admin/DownloadsServerNotice";
import { LibraryAccessSelector } from "@/components/LibraryAccessSelector";
import { StreamBitrateLimitInput } from "@/components/StreamBitrateLimitInput";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import {
  PLAYBACK_QUALITY_OPTIONS,
  formatPlaybackQualityPreset,
  playbackQualityPresetFromValue,
  playbackQualityValueFromPreset,
  type PlaybackQualityPreset,
} from "@/lib/playback-quality";
import { formatStreamBitrateLimit } from "@/lib/streamBitrateLimit";
import { useT, type MessageKey, type Translate } from "@/i18n";

// Per-user policy overrides. null = no override, so the field takes its
// default (see PolicyDefaultSource); a concrete value is an explicit override
// in either direction.
export interface UserPolicyState {
  libraryIDs: number[] | null;
  maxPlaybackQuality: string | null;
  maxStreams: number | null;
  maxTranscodes: number | null;
  maxRemoteStreamBitrateKbps: number | null;
  maxLocalStreamBitrateKbps: number | null;
  maxProfiles: number | null;
  transcodeAllowed: boolean | null;
  audioTranscodeAllowed: boolean | null;
  downloadAllowed: boolean | null;
  downloadTranscodeAllowed: boolean | null;
  requestsAllowed: boolean | null;
}

// The one place the state keys and their API field names are paired up; the
// helpers below are all derived from it so a new policy field is added once.
const POLICY_FIELDS = {
  libraryIDs: "library_ids",
  maxPlaybackQuality: "max_playback_quality",
  maxStreams: "max_streams",
  maxTranscodes: "max_transcodes",
  maxRemoteStreamBitrateKbps: "max_remote_stream_bitrate_kbps",
  maxLocalStreamBitrateKbps: "max_local_stream_bitrate_kbps",
  maxProfiles: "max_profiles",
  transcodeAllowed: "transcode_allowed",
  audioTranscodeAllowed: "audio_transcode_allowed",
  downloadAllowed: "download_allowed",
  downloadTranscodeAllowed: "download_transcode_allowed",
  requestsAllowed: "requests_allowed",
} as const satisfies Record<keyof UserPolicyState, keyof AdminUser>;

// Update payload: every policy field is sent explicitly — a value stores an
// override, null clears it back to inherit.
type PolicyUpdatePayload = {
  [K in keyof UserPolicyState as (typeof POLICY_FIELDS)[K]]: UserPolicyState[K];
};

// Create payload: only overridden fields are sent; absent fields inherit.
type PolicyCreatePayload = {
  [K in keyof PolicyUpdatePayload]?: Exclude<PolicyUpdatePayload[K], null>;
};

export function policyStateFromUser(user: AdminUser | null): UserPolicyState {
  return Object.fromEntries(
    Object.entries(POLICY_FIELDS).map(([key, field]) => [key, user?.[field] ?? null]),
  ) as unknown as UserPolicyState;
}

export function policyUpdateFields(state: UserPolicyState): PolicyUpdatePayload {
  return Object.fromEntries(
    Object.entries(POLICY_FIELDS).map(([key, field]) => [
      field,
      state[key as keyof UserPolicyState],
    ]),
  ) as PolicyUpdatePayload;
}

export function policyCreateFields(state: UserPolicyState): PolicyCreatePayload {
  return Object.fromEntries(
    Object.entries(policyUpdateFields(state)).filter(([, value]) => value !== null),
  ) as PolicyCreatePayload;
}

// What an inheriting field resolves to. Same shape as the server's resolved
// policy minus permissions, which have no inherit control here.
export type PolicyInheritHints = Partial<Omit<AdminUserEffectivePolicy, "permissions">>;

// Inherit hints for the role and group currently selected in the form — not
// the ones the account was last saved with, so the hints follow the pickers
// instead of going stale. An admin or an ungrouped account takes the server's
// built-in defaults. Returns undefined while those or the selected group are
// not loaded (or the group was since deleted) so callers can fall back.
export function policyInheritHints(
  role: string,
  accessGroupID: number | null,
  accessGroups: AccessGroup[],
  defaults: AdminPolicyDefaults | undefined,
): PolicyInheritHints | undefined {
  if (role === "admin") return defaults?.admin;
  if (accessGroupID === null) return defaults?.ungrouped;
  const group = accessGroups.find((candidate) => candidate.id === accessGroupID);
  if (group === undefined) return undefined;
  return {
    library_ids: group.library_ids,
    max_playback_quality: group.max_playback_quality,
    max_streams: group.max_streams,
    max_transcodes: group.max_transcodes,
    max_remote_stream_bitrate_kbps: group.max_remote_stream_bitrate_kbps,
    max_local_stream_bitrate_kbps: group.max_local_stream_bitrate_kbps,
    max_profiles: group.max_profiles,
    transcode_allowed: group.transcode_allowed,
    audio_transcode_allowed: group.audio_transcode_allowed,
    download_allowed: group.download_allowed,
    download_transcode_allowed: group.download_transcode_allowed,
    requests_allowed: group.requests_allowed,
  };
}

// For the account's saved group, effective_policy is the freshest resolved
// value for fields that already inherit. An overridden field instead needs
// the group-only value it would inherit after the override is cleared.
export function savedUserPolicyInheritHints(
  user: AdminUser,
  groupHints: PolicyInheritHints | undefined,
): PolicyInheritHints {
  return Object.fromEntries(
    Object.values(POLICY_FIELDS).flatMap((field) => {
      const value = user[field] === null ? user.effective_policy[field] : groupHints?.[field];
      return value === undefined ? [] : [[field, value]];
    }),
  ) as PolicyInheritHints;
}

// Admins are never grouped: the server clears access_group_id for the admin
// role (auth.Repository.CreateUser/UpdateUser), so every form that shows or
// submits a group for a user has to mirror that rule locally.
export function effectiveAccessGroupID(role: string, accessGroupID: number | null): number | null {
  return role === "admin" ? null : accessGroupID;
}

// Where a field that is not overridden gets its value. Only a grouped account
// inherits; an admin gets the server's admin defaults (full access) and an
// account outside every group the no-group defaults, so their fields name that
// source instead of a group.
export type PolicyDefaultSource = "group" | "admin" | "server";

export function policyDefaultSource(
  role: string,
  accessGroupID: number | null,
): PolicyDefaultSource {
  if (role === "admin") return "admin";
  return accessGroupID === null ? "server" : "group";
}

const DEFAULT_SOURCE_TEXT: Record<
  PolicyDefaultSource,
  {
    value: MessageKey;
    unknown: MessageKey;
    allLibraries: MessageKey;
    limitHelp: MessageKey;
    limitHelpMin: MessageKey;
    quality: MessageKey;
  }
> = {
  group: {
    value: "web.profiles.userPolicy.groupValue",
    unknown: "web.profiles.userPolicy.groupUnknown",
    allLibraries: "web.profiles.userPolicy.groupAllLibraries",
    limitHelp: "web.profiles.userPolicy.groupLimitHelp",
    limitHelpMin: "web.profiles.userPolicy.groupLimitHelpMin",
    quality: "web.profiles.userPolicy.groupQuality",
  },
  admin: {
    value: "web.profiles.userPolicy.adminValue",
    unknown: "web.profiles.userPolicy.adminDefault",
    allLibraries: "web.profiles.userPolicy.adminDefault",
    limitHelp: "web.profiles.userPolicy.adminLimitHelp",
    limitHelpMin: "web.profiles.userPolicy.adminLimitHelpMin",
    quality: "web.profiles.userPolicy.adminQuality",
  },
  server: {
    value: "web.profiles.userPolicy.serverValue",
    unknown: "web.profiles.userPolicy.serverDefault",
    allLibraries: "web.profiles.userPolicy.serverDefault",
    limitHelp: "web.profiles.userPolicy.serverLimitHelp",
    limitHelpMin: "web.profiles.userPolicy.serverLimitHelpMin",
    quality: "web.profiles.userPolicy.serverQuality",
  },
};

interface PolicyContext {
  state: UserPolicyState;
  onChange: (state: UserPolicyState) => void;
  // Where the fields take their value from when they are not overridden.
  source: PolicyDefaultSource;
  // What those fields currently evaluate to, shown next to the source. Absent
  // when unknown.
  effective?: PolicyInheritHints;
  // Locks the menus. A disabled <fieldset> blocks the native controls, but
  // Radix Select opens on pointerdown and only honors its own prop.
  disabled?: boolean;
}

// Raw text of limit fields whose box holds no valid number. A cleared or
// half-typed box is not in UserPolicyState, which keeps the last valid value,
// so the form keeps these drafts itself and refuses to save while any remain,
// even when the Limits tab is not mounted and native validation cannot see the
// box. Keeping the text, not just a flag, lets a remounted field show it again.
export type LimitPolicyKey =
  | "maxStreams"
  | "maxTranscodes"
  | "maxRemoteStreamBitrate"
  | "maxLocalStreamBitrate"
  | "maxProfiles";

export interface LimitValidity {
  invalidLimitDrafts: ReadonlyMap<LimitPolicyKey, string>;
  // `undefined` clears the entry once the box holds a valid number again.
  onInvalidLimitDraftChange: (key: LimitPolicyKey, draft: string | undefined) => void;
}

function defaultHint(
  t: Translate,
  source: PolicyDefaultSource,
  effectiveText: string | undefined,
): string {
  const text = DEFAULT_SOURCE_TEXT[source];
  return effectiveText === undefined ? t(text.unknown) : t(text.value, { value: effectiveText });
}

const INHERIT = "inherit" as const;

function BooleanPolicyRow({
  label,
  description,
  value,
  onValueChange,
  source,
  effectiveValue,
  disabled,
}: {
  label: string;
  description?: string;
  value: boolean | null;
  onValueChange: (value: boolean | null) => void;
  source: PolicyDefaultSource;
  effectiveValue?: boolean;
  disabled?: boolean;
}) {
  const t = useT();
  const id = useId();
  const selectValue = value === null ? INHERIT : value ? "allowed" : "blocked";
  return (
    <div className="border-border flex flex-wrap items-center justify-between gap-x-3 gap-y-2 rounded-md border px-3 py-2">
      <div className="min-w-32 flex-1 break-words">
        <Label htmlFor={id}>{label}</Label>
        {description && <p className="text-muted-foreground text-xs">{description}</p>}
      </div>
      <Select
        value={selectValue}
        onValueChange={(next) => onValueChange(next === INHERIT ? null : next === "allowed")}
        disabled={disabled}
      >
        <SelectTrigger
          id={id}
          className="max-w-full min-w-40 shrink-0 text-start whitespace-normal data-[size=default]:h-auto data-[size=default]:min-h-9 *:data-[slot=select-value]:line-clamp-none"
        >
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value={INHERIT}>
            {defaultHint(
              t,
              source,
              effectiveValue === undefined
                ? undefined
                : effectiveValue
                  ? t("web.profiles.userPolicy.allowed")
                  : t("web.profiles.userPolicy.notAllowed"),
            )}
          </SelectItem>
          <SelectItem value="allowed">{t("web.profiles.userPolicy.allowed")}</SelectItem>
          <SelectItem value="blocked">{t("web.profiles.userPolicy.notAllowed")}</SelectItem>
        </SelectContent>
      </Select>
    </div>
  );
}

function limitDraftValue(draft: string, min: number): number | null {
  if (draft.trim() === "") return null;
  const parsed = Number(draft);
  if (!Number.isInteger(parsed) || parsed < min) return null;
  return parsed;
}

// Label row with the Override switch; while not overridden the field shows
// its default and where it comes from instead of its control.
function PolicyOverrideField({
  id,
  label,
  overridden,
  onOverriddenChange,
  source,
  defaultText,
  children,
}: {
  id: string;
  label: string;
  overridden: boolean;
  onOverriddenChange: (checked: boolean) => void;
  source: PolicyDefaultSource;
  defaultText: string | undefined;
  children: ReactNode;
}) {
  const t = useT();
  const overrideId = `${id}-override`;
  return (
    <div className="space-y-1">
      <div className="flex items-center justify-between">
        <Label htmlFor={id}>{label}</Label>
        <div className="flex items-center gap-2">
          <Label htmlFor={overrideId} className="text-muted-foreground text-xs">
            {t("web.profiles.userPolicy.override")}
          </Label>
          <Switch id={overrideId} checked={overridden} onCheckedChange={onOverriddenChange} />
        </div>
      </div>
      {overridden ? (
        children
      ) : (
        <p className="text-muted-foreground border-border rounded-md border border-dashed px-3 py-2 text-sm">
          {defaultHint(t, source, defaultText)}
        </p>
      )}
    </div>
  );
}

function LimitPolicyField({
  label,
  value,
  onValueChange,
  source,
  effectiveValue,
  min = 0,
  invalidDraft,
  onInvalidDraftChange,
}: {
  label: string;
  value: number | null;
  onValueChange: (value: number | null) => void;
  source: PolicyDefaultSource;
  effectiveValue?: number;
  /** The lowest value; with 0 allowed it means unlimited. */
  min?: number;
  // The invalid text left behind when the field last unmounted, or undefined.
  // The field reopens overridden with that text so it shows the pending edit
  // instead of the stale saved value.
  invalidDraft: string | undefined;
  onInvalidDraftChange: (draft: string | undefined) => void;
}) {
  const t = useT();
  const id = useId();
  // Override is tracked locally because "overriding, but nothing typed yet" has
  // no representation in UserPolicyState: while the box is empty the field
  // keeps inheriting rather than pinning 0, which would mean unlimited.
  const [overridden, setOverridden] = useState(value !== null || invalidDraft !== undefined);
  // The raw string stays local so a cleared or half-typed box is an unsaved
  // edit instead of collapsing to 0 or NaN.
  const [draft, setDraft] = useState(() => invalidDraft ?? (value === null ? "" : String(value)));
  const draftValue = limitDraftValue(draft, min);
  const help =
    draftValue === null
      ? min > 0
        ? t(DEFAULT_SOURCE_TEXT[source].limitHelpMin, { min: min })
        : t(DEFAULT_SOURCE_TEXT[source].limitHelp)
      : min === 0
        ? t("web.profiles.userPolicy.zeroUnlimited")
        : undefined;

  function handleOverrideChange(checked: boolean) {
    setOverridden(checked);
    if (!checked) {
      setDraft("");
      onValueChange(null);
      onInvalidDraftChange(undefined);
      return;
    }
    // Seed the value the field already resolves to. With no hint available the
    // box starts empty and the field keeps inheriting until the admin types a
    // value, so an unknown limit is never silently saved as unlimited.
    setDraft(effectiveValue === undefined ? "" : String(effectiveValue));
    onValueChange(effectiveValue ?? null);
    onInvalidDraftChange(effectiveValue === undefined ? "" : undefined);
  }

  function handleDraftChange(raw: string) {
    setDraft(raw);
    const parsed = limitDraftValue(raw, min);
    onInvalidDraftChange(parsed === null ? raw : undefined);
    if (parsed === null) return;
    onValueChange(parsed);
  }

  return (
    <PolicyOverrideField
      id={id}
      label={label}
      overridden={overridden}
      onOverriddenChange={handleOverrideChange}
      source={source}
      defaultText={
        effectiveValue === undefined
          ? undefined
          : effectiveValue === 0
            ? t("web.profiles.userPolicy.unlimited")
            : String(effectiveValue)
      }
    >
      <Input
        id={id}
        type="number"
        min={min}
        step={1}
        required
        value={draft}
        onChange={(event) => handleDraftChange(event.target.value)}
      />
      {help && <p className="text-muted-foreground text-xs">{help}</p>}
    </PolicyOverrideField>
  );
}

function StreamBitratePolicyField({
  label,
  value,
  onValueChange,
  source,
  effectiveValue,
  disabled,
  invalidDraft,
  onInvalidDraftChange,
}: {
  label: string;
  value: number | null;
  onValueChange: (value: number | null) => void;
  source: PolicyDefaultSource;
  effectiveValue?: number;
  disabled?: boolean;
  // Same contract as LimitPolicyField: the custom Mbps text left invalid when
  // the field last unmounted, or undefined.
  invalidDraft: string | undefined;
  onInvalidDraftChange: (draft: string | undefined) => void;
}) {
  const id = useId();
  // Same override model as LimitPolicyField: turning Override on seeds the
  // value the field already resolves to, and with no hint the field keeps its
  // default until the admin picks a limit. That unpicked state is reported as
  // an empty draft, so the form refuses to save it from another tab too.
  const [overridden, setOverridden] = useState(value !== null || invalidDraft !== undefined);

  function handleOverrideChange(checked: boolean) {
    setOverridden(checked);
    onValueChange(checked ? (effectiveValue ?? null) : null);
    onInvalidDraftChange(checked && effectiveValue === undefined ? "" : undefined);
  }

  return (
    <PolicyOverrideField
      id={id}
      label={label}
      overridden={overridden}
      onOverriddenChange={handleOverrideChange}
      source={source}
      defaultText={
        effectiveValue === undefined ? undefined : formatStreamBitrateLimit(effectiveValue)
      }
    >
      <StreamBitrateLimitInput
        id={id}
        label={label}
        value={value}
        disabled={disabled}
        // A custom box without a valid value keeps the last one in the policy
        // state; the form refuses to save while its invalid draft remains.
        onValueChange={(kbps) => {
          if (kbps !== null) onValueChange(kbps);
        }}
        invalidDraft={invalidDraft}
        onInvalidDraftChange={onInvalidDraftChange}
      />
    </PolicyOverrideField>
  );
}

// Access-tab policy fields: library scope plus the download/request gates.
export function PolicyAccessFields({
  state,
  onChange,
  source,
  effective,
  libraries,
  disabled,
}: PolicyContext & { libraries: Library[] }) {
  const t = useT();
  return (
    <>
      <LibraryAccessSelector
        libraries={libraries}
        value={state.libraryIDs}
        onChange={(libraryIDs) => onChange({ ...state, libraryIDs })}
        allLabel={t(DEFAULT_SOURCE_TEXT[source].allLibraries)}
        emptyHint={defaultHint(
          t,
          source,
          effective?.library_ids === undefined
            ? undefined
            : effective.library_ids === null
              ? t("web.profiles.libraryAccess.allLibraries")
              : t("web.profiles.libraries.count", {
                  count: effective.library_ids.length,
                  count_text: effective.library_ids.length,
                }),
        )}
      />
      <DownloadsServerNotice />
      <div className="grid gap-2 sm:grid-cols-2">
        <BooleanPolicyRow
          label={t("web.profiles.userPolicy.downloads")}
          value={state.downloadAllowed}
          onValueChange={(downloadAllowed) => onChange({ ...state, downloadAllowed })}
          source={source}
          effectiveValue={effective?.download_allowed}
          disabled={disabled}
        />
        <BooleanPolicyRow
          label={t("web.profiles.userPolicy.downloadTranscodes")}
          value={state.downloadTranscodeAllowed}
          onValueChange={(downloadTranscodeAllowed) =>
            onChange({ ...state, downloadTranscodeAllowed })
          }
          source={source}
          effectiveValue={effective?.download_transcode_allowed}
          disabled={disabled}
        />
      </div>
      <BooleanPolicyRow
        label={t("web.profiles.userPolicy.mediaRequests")}
        description={t("web.profiles.userPolicy.mediaRequestsHint")}
        value={state.requestsAllowed}
        onValueChange={(requestsAllowed) => onChange({ ...state, requestsAllowed })}
        source={source}
        effectiveValue={effective?.requests_allowed}
        disabled={disabled}
      />
    </>
  );
}

// Limits-tab policy fields: stream/transcode ceilings and the quality gate.
export function PolicyLimitFields({
  state,
  onChange,
  source,
  effective,
  disabled,
  invalidLimitDrafts,
  onInvalidLimitDraftChange,
}: PolicyContext & LimitValidity) {
  const t = useT();
  const qualityId = useId();
  const qualityValue: PlaybackQualityPreset | typeof INHERIT =
    state.maxPlaybackQuality === null
      ? INHERIT
      : playbackQualityPresetFromValue(state.maxPlaybackQuality);
  return (
    <>
      <div className="grid gap-3 sm:grid-cols-2">
        <LimitPolicyField
          label={t("web.profiles.userPolicy.maxStreams")}
          value={state.maxStreams}
          onValueChange={(maxStreams) => onChange({ ...state, maxStreams })}
          source={source}
          effectiveValue={effective?.max_streams}
          invalidDraft={invalidLimitDrafts.get("maxStreams")}
          onInvalidDraftChange={(draft) => onInvalidLimitDraftChange("maxStreams", draft)}
        />
        <LimitPolicyField
          label={t("web.profiles.userPolicy.maxTranscodes")}
          value={state.maxTranscodes}
          onValueChange={(maxTranscodes) => onChange({ ...state, maxTranscodes })}
          source={source}
          effectiveValue={effective?.max_transcodes}
          invalidDraft={invalidLimitDrafts.get("maxTranscodes")}
          onInvalidDraftChange={(draft) => onInvalidLimitDraftChange("maxTranscodes", draft)}
        />
        <StreamBitratePolicyField
          label={t("web.profiles.userPolicy.maxRemoteBitrate")}
          value={state.maxRemoteStreamBitrateKbps}
          onValueChange={(maxRemoteStreamBitrateKbps) =>
            onChange({ ...state, maxRemoteStreamBitrateKbps })
          }
          source={source}
          effectiveValue={effective?.max_remote_stream_bitrate_kbps}
          disabled={disabled}
          invalidDraft={invalidLimitDrafts.get("maxRemoteStreamBitrate")}
          onInvalidDraftChange={(draft) =>
            onInvalidLimitDraftChange("maxRemoteStreamBitrate", draft)
          }
        />
        <StreamBitratePolicyField
          label={t("web.profiles.userPolicy.maxLocalBitrate")}
          value={state.maxLocalStreamBitrateKbps}
          onValueChange={(maxLocalStreamBitrateKbps) =>
            onChange({ ...state, maxLocalStreamBitrateKbps })
          }
          source={source}
          effectiveValue={effective?.max_local_stream_bitrate_kbps}
          disabled={disabled}
          invalidDraft={invalidLimitDrafts.get("maxLocalStreamBitrate")}
          onInvalidDraftChange={(draft) =>
            onInvalidLimitDraftChange("maxLocalStreamBitrate", draft)
          }
        />
      </div>
      <div className="grid gap-2 sm:grid-cols-2">
        <BooleanPolicyRow
          label={t("web.profiles.userPolicy.videoTranscoding")}
          value={state.transcodeAllowed}
          onValueChange={(transcodeAllowed) => onChange({ ...state, transcodeAllowed })}
          source={source}
          effectiveValue={effective?.transcode_allowed}
          disabled={disabled}
        />
        <BooleanPolicyRow
          label={t("web.profiles.userPolicy.audioTranscoding")}
          description={t("web.profiles.userPolicy.audioTranscodingHint")}
          value={state.audioTranscodeAllowed}
          onValueChange={(audioTranscodeAllowed) => onChange({ ...state, audioTranscodeAllowed })}
          source={source}
          effectiveValue={effective?.audio_transcode_allowed}
          disabled={disabled}
        />
      </div>
      <div className="space-y-1">
        <Label htmlFor={qualityId}>{t("web.profiles.userPolicy.maxPlaybackQuality")}</Label>
        <Select
          disabled={disabled}
          value={qualityValue}
          onValueChange={(value) =>
            onChange({
              ...state,
              maxPlaybackQuality:
                value === INHERIT
                  ? null
                  : playbackQualityValueFromPreset(value as PlaybackQualityPreset),
            })
          }
        >
          <SelectTrigger id={qualityId} className="w-full">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={INHERIT}>
              {defaultHint(
                t,
                source,
                effective?.max_playback_quality === undefined
                  ? undefined
                  : formatPlaybackQualityPreset(effective.max_playback_quality),
              )}
            </SelectItem>
            {PLAYBACK_QUALITY_OPTIONS.map((option) => (
              <SelectItem key={option.value} value={option.value}>
                {option.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <p className="text-muted-foreground text-xs">
          {qualityValue === INHERIT
            ? t(DEFAULT_SOURCE_TEXT[source].quality)
            : PLAYBACK_QUALITY_OPTIONS.find((option) => option.value === qualityValue)?.description}
        </p>
      </div>
      <LimitPolicyField
        label={t("web.profiles.userPolicy.maxProfiles")}
        min={1}
        value={state.maxProfiles}
        onValueChange={(maxProfiles) => onChange({ ...state, maxProfiles })}
        source={source}
        effectiveValue={effective?.max_profiles}
        invalidDraft={invalidLimitDrafts.get("maxProfiles")}
        onInvalidDraftChange={(draft) => onInvalidLimitDraftChange("maxProfiles", draft)}
      />
    </>
  );
}
