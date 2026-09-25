import { useId, useState, type ReactNode } from "react";

import type { AccessGroup, AdminUser, AdminUserEffectivePolicy, Library } from "@/api/types";
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
export type PolicyInheritHints = Omit<AdminUserEffectivePolicy, "permissions">;

// Mirrors access.NoGroupPolicy(): the layer under an account that belongs to
// no access group. Keep in sync with internal/access/groups.go.
const NO_GROUP_POLICY: PolicyInheritHints = {
  library_ids: null,
  max_playback_quality: "",
  max_streams: 0,
  max_transcodes: 0,
  max_remote_stream_bitrate_kbps: 0,
  max_local_stream_bitrate_kbps: 0,
  transcode_allowed: true,
  audio_transcode_allowed: true,
  download_allowed: true,
  download_transcode_allowed: false,
  requests_allowed: true,
};

// Inherit hints for the group currently selected in the form — not the group
// the account was last saved with, so the hints follow the picker instead of
// going stale. Returns undefined when the selected group is not in the loaded
// list (still loading, or since deleted) so callers can fall back.
export function policyInheritHints(
  accessGroupID: number | null,
  accessGroups: AccessGroup[],
): PolicyInheritHints | undefined {
  if (accessGroupID === null) return NO_GROUP_POLICY;
  const group = accessGroups.find((candidate) => candidate.id === accessGroupID);
  if (group === undefined) return undefined;
  return {
    library_ids: group.library_ids,
    max_playback_quality: group.max_playback_quality,
    max_streams: group.max_streams,
    max_transcodes: group.max_transcodes,
    max_remote_stream_bitrate_kbps: group.max_remote_stream_bitrate_kbps,
    max_local_stream_bitrate_kbps: group.max_local_stream_bitrate_kbps,
    transcode_allowed: group.transcode_allowed,
    audio_transcode_allowed: group.audio_transcode_allowed,
    download_allowed: group.download_allowed,
    download_transcode_allowed: group.download_transcode_allowed,
    requests_allowed: group.requests_allowed,
  };
}

// Admins are never grouped: the server clears access_group_id for the admin
// role (auth.Repository.CreateUser/UpdateUser), so every form that shows or
// submits a group for a user has to mirror that rule locally.
export function effectiveAccessGroupID(role: string, accessGroupID: number | null): number | null {
  return role === "admin" ? null : accessGroupID;
}

// Where a field that is not overridden gets its value. Only a grouped account
// inherits; an admin or an account outside every group gets the server's fixed
// no-group defaults, so its fields name that source instead of a group.
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
  { prefix: string; unknown: string; allLibraries: string; revert: string; quality: string }
> = {
  group: {
    prefix: "Inherited",
    unknown: "Inherited from group",
    allLibraries: "Inherit from group",
    revert: "inherit",
    quality: "Uses the access group's quality ceiling.",
  },
  admin: {
    prefix: "Admin default",
    unknown: "Admin default",
    allLibraries: "Admin default",
    revert: "use the admin default",
    quality: "Uses the admin default quality ceiling.",
  },
  server: {
    prefix: "Server default",
    unknown: "Server default",
    allLibraries: "Server default",
    revert: "use the server default",
    quality: "Uses the server default quality ceiling.",
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
}

function defaultHint(source: PolicyDefaultSource, effectiveText: string | undefined): string {
  const text = DEFAULT_SOURCE_TEXT[source];
  return effectiveText === undefined ? text.unknown : `${text.prefix}: ${effectiveText}`;
}

const INHERIT = "inherit" as const;

function BooleanPolicyRow({
  label,
  description,
  value,
  onValueChange,
  source,
  effectiveValue,
}: {
  label: string;
  description?: string;
  value: boolean | null;
  onValueChange: (value: boolean | null) => void;
  source: PolicyDefaultSource;
  effectiveValue?: boolean;
}) {
  const id = useId();
  const selectValue = value === null ? INHERIT : value ? "allowed" : "blocked";
  return (
    <div className="border-border flex items-center justify-between gap-3 rounded-md border px-3 py-2">
      <div className="min-w-0">
        <Label htmlFor={id}>{label}</Label>
        {description && <p className="text-muted-foreground text-xs">{description}</p>}
      </div>
      <Select
        value={selectValue}
        onValueChange={(next) => onValueChange(next === INHERIT ? null : next === "allowed")}
      >
        <SelectTrigger id={id} className="w-40 shrink-0">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value={INHERIT}>
            {defaultHint(
              source,
              effectiveValue === undefined ? undefined : effectiveValue ? "Allowed" : "Not allowed",
            )}
          </SelectItem>
          <SelectItem value="allowed">Allowed</SelectItem>
          <SelectItem value="blocked">Not allowed</SelectItem>
        </SelectContent>
      </Select>
    </div>
  );
}

function limitDraftValue(draft: string): number | null {
  if (draft.trim() === "") return null;
  const parsed = Number(draft);
  if (!Number.isInteger(parsed) || parsed < 0) return null;
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
  const overrideId = `${id}-override`;
  return (
    <div className="space-y-1">
      <div className="flex items-center justify-between">
        <Label htmlFor={id}>{label}</Label>
        <div className="flex items-center gap-2">
          <Label htmlFor={overrideId} className="text-muted-foreground text-xs">
            Override
          </Label>
          <Switch id={overrideId} checked={overridden} onCheckedChange={onOverriddenChange} />
        </div>
      </div>
      {overridden ? (
        children
      ) : (
        <p className="text-muted-foreground border-border rounded-md border border-dashed px-3 py-2 text-sm">
          {defaultHint(source, defaultText)}
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
}: {
  label: string;
  value: number | null;
  onValueChange: (value: number | null) => void;
  source: PolicyDefaultSource;
  effectiveValue?: number;
}) {
  const id = useId();
  // Override is tracked locally because "overriding, but nothing typed yet" has
  // no representation in UserPolicyState: while the box is empty the field
  // keeps inheriting rather than pinning 0, which would mean unlimited.
  const [overridden, setOverridden] = useState(value !== null);
  // The raw string stays local so a cleared or half-typed box is an unsaved
  // edit instead of collapsing to 0 or NaN.
  const [draft, setDraft] = useState(() => (value === null ? "" : String(value)));
  const draftValue = limitDraftValue(draft);

  function handleOverrideChange(checked: boolean) {
    setOverridden(checked);
    if (!checked) {
      setDraft("");
      onValueChange(null);
      return;
    }
    // Seed the value the field already resolves to. With no hint available the
    // box starts empty and the field keeps inheriting until the admin types a
    // value, so an unknown limit is never silently saved as unlimited.
    setDraft(effectiveValue === undefined ? "" : String(effectiveValue));
    onValueChange(effectiveValue ?? null);
  }

  function handleDraftChange(raw: string) {
    setDraft(raw);
    const parsed = limitDraftValue(raw);
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
            ? "Unlimited"
            : String(effectiveValue)
      }
    >
      <Input
        id={id}
        type="number"
        min={0}
        step={1}
        required
        value={draft}
        onChange={(event) => handleDraftChange(event.target.value)}
      />
      <p className="text-muted-foreground text-xs">
        {draftValue === null
          ? `Enter a whole number, or turn Override off to ${DEFAULT_SOURCE_TEXT[source].revert}.`
          : "0 = unlimited"}
      </p>
    </PolicyOverrideField>
  );
}

function StreamBitratePolicyField({
  label,
  value,
  onValueChange,
  source,
  effectiveValue,
}: {
  label: string;
  value: number | null;
  onValueChange: (value: number | null) => void;
  source: PolicyDefaultSource;
  effectiveValue?: number;
}) {
  const id = useId();
  // Same override model as LimitPolicyField: turning Override on seeds the
  // value the field already resolves to, and with no hint the field keeps its
  // default until the admin picks a limit.
  const [overridden, setOverridden] = useState(value !== null);

  function handleOverrideChange(checked: boolean) {
    setOverridden(checked);
    onValueChange(checked ? (effectiveValue ?? null) : null);
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
        // A custom box without a valid value keeps the last one; the form's
        // required/pattern validation blocks saving until it is fixed.
        onValueChange={(kbps) => {
          if (kbps !== null) onValueChange(kbps);
        }}
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
}: PolicyContext & { libraries: Library[] }) {
  return (
    <>
      <LibraryAccessSelector
        libraries={libraries}
        value={state.libraryIDs}
        onChange={(libraryIDs) => onChange({ ...state, libraryIDs })}
        allLabel={DEFAULT_SOURCE_TEXT[source].allLibraries}
        emptyHint={defaultHint(
          source,
          effective === undefined
            ? undefined
            : effective.library_ids === null
              ? "All libraries"
              : `${effective.library_ids.length} libraries`,
        )}
      />
      <div className="grid gap-2 sm:grid-cols-2">
        <BooleanPolicyRow
          label="Downloads"
          value={state.downloadAllowed}
          onValueChange={(downloadAllowed) => onChange({ ...state, downloadAllowed })}
          source={source}
          effectiveValue={effective?.download_allowed}
        />
        <BooleanPolicyRow
          label="Download Transcodes"
          value={state.downloadTranscodeAllowed}
          onValueChange={(downloadTranscodeAllowed) =>
            onChange({ ...state, downloadTranscodeAllowed })
          }
          source={source}
          effectiveValue={effective?.download_transcode_allowed}
        />
      </div>
      <BooleanPolicyRow
        label="Media Requests"
        description="Request new movies and series when requests are enabled."
        value={state.requestsAllowed}
        onValueChange={(requestsAllowed) => onChange({ ...state, requestsAllowed })}
        source={source}
        effectiveValue={effective?.requests_allowed}
      />
    </>
  );
}

// Limits-tab policy fields: stream/transcode ceilings and the quality gate.
export function PolicyLimitFields({ state, onChange, source, effective }: PolicyContext) {
  const qualityId = useId();
  const qualityValue: PlaybackQualityPreset | typeof INHERIT =
    state.maxPlaybackQuality === null
      ? INHERIT
      : playbackQualityPresetFromValue(state.maxPlaybackQuality);
  return (
    <>
      <div className="grid gap-3 sm:grid-cols-2">
        <LimitPolicyField
          label="Max Streams"
          value={state.maxStreams}
          onValueChange={(maxStreams) => onChange({ ...state, maxStreams })}
          source={source}
          effectiveValue={effective?.max_streams}
        />
        <LimitPolicyField
          label="Max Transcodes"
          value={state.maxTranscodes}
          onValueChange={(maxTranscodes) => onChange({ ...state, maxTranscodes })}
          source={source}
          effectiveValue={effective?.max_transcodes}
        />
        <StreamBitratePolicyField
          label="Max remote stream bitrate"
          value={state.maxRemoteStreamBitrateKbps}
          onValueChange={(maxRemoteStreamBitrateKbps) =>
            onChange({ ...state, maxRemoteStreamBitrateKbps })
          }
          source={source}
          effectiveValue={effective?.max_remote_stream_bitrate_kbps}
        />
        <StreamBitratePolicyField
          label="Max local stream bitrate"
          value={state.maxLocalStreamBitrateKbps}
          onValueChange={(maxLocalStreamBitrateKbps) =>
            onChange({ ...state, maxLocalStreamBitrateKbps })
          }
          source={source}
          effectiveValue={effective?.max_local_stream_bitrate_kbps}
        />
      </div>
      <div className="grid gap-2 sm:grid-cols-2">
        <BooleanPolicyRow
          label="Video Transcoding"
          value={state.transcodeAllowed}
          onValueChange={(transcodeAllowed) => onChange({ ...state, transcodeAllowed })}
          source={source}
          effectiveValue={effective?.transcode_allowed}
        />
        <BooleanPolicyRow
          label="Audio Transcoding"
          description="Audio conversion without video encoding."
          value={state.audioTranscodeAllowed}
          onValueChange={(audioTranscodeAllowed) => onChange({ ...state, audioTranscodeAllowed })}
          source={source}
          effectiveValue={effective?.audio_transcode_allowed}
        />
      </div>
      <div className="space-y-1">
        <Label htmlFor={qualityId}>Max Playback Quality</Label>
        <Select
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
                source,
                effective === undefined
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
            ? DEFAULT_SOURCE_TEXT[source].quality
            : PLAYBACK_QUALITY_OPTIONS.find((option) => option.value === qualityValue)?.description}
        </p>
      </div>
    </>
  );
}
