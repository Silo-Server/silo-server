import { useId, useState } from "react";

import type { AccessGroup, AdminUser, AdminUserEffectivePolicy, Library } from "@/api/types";
import { LibraryAccessSelector } from "@/components/LibraryAccessSelector";
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

// Per-user policy overrides. null = inherit the access group's value; a
// concrete value is an explicit override in either direction.
export interface UserPolicyState {
  libraryIDs: number[] | null;
  maxPlaybackQuality: string | null;
  maxStreams: number | null;
  maxTranscodes: number | null;
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
    transcode_allowed: group.transcode_allowed,
    audio_transcode_allowed: group.audio_transcode_allowed,
    download_allowed: group.download_allowed,
    download_transcode_allowed: group.download_transcode_allowed,
    requests_allowed: group.requests_allowed,
  };
}

interface PolicyContext {
  state: UserPolicyState;
  onChange: (state: UserPolicyState) => void;
  // What the fields inherit when they are not overridden, used to show what an
  // inheriting field currently evaluates to. Absent when unknown.
  effective?: PolicyInheritHints;
}

function inheritHint(effectiveText: string | undefined): string {
  return effectiveText === undefined ? "Inherited from group" : `Inherited: ${effectiveText}`;
}

const INHERIT = "inherit" as const;

function BooleanPolicyRow({
  label,
  description,
  value,
  onValueChange,
  effectiveValue,
}: {
  label: string;
  description?: string;
  value: boolean | null;
  onValueChange: (value: boolean | null) => void;
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
            {inheritHint(
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

function LimitPolicyField({
  label,
  value,
  onValueChange,
  effectiveValue,
}: {
  label: string;
  value: number | null;
  onValueChange: (value: number | null) => void;
  effectiveValue?: number;
}) {
  const id = useId();
  const overrideId = `${id}-override`;
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
    <div className="space-y-1">
      <div className="flex items-center justify-between">
        <Label htmlFor={id}>{label}</Label>
        <div className="flex items-center gap-2">
          <Label htmlFor={overrideId} className="text-muted-foreground text-xs">
            Override
          </Label>
          <Switch id={overrideId} checked={overridden} onCheckedChange={handleOverrideChange} />
        </div>
      </div>
      {overridden ? (
        <>
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
              ? "Enter a whole number, or turn Override off to inherit."
              : "0 = unlimited"}
          </p>
        </>
      ) : (
        <p className="text-muted-foreground border-border rounded-md border border-dashed px-3 py-2 text-sm">
          {inheritHint(
            effectiveValue === undefined
              ? undefined
              : effectiveValue === 0
                ? "Unlimited"
                : String(effectiveValue),
          )}
        </p>
      )}
    </div>
  );
}

// Access-tab policy fields: library scope plus the download/request gates.
export function PolicyAccessFields({
  state,
  onChange,
  effective,
  libraries,
}: PolicyContext & { libraries: Library[] }) {
  return (
    <>
      <LibraryAccessSelector
        libraries={libraries}
        value={state.libraryIDs}
        onChange={(libraryIDs) => onChange({ ...state, libraryIDs })}
        allLabel="Inherit from group"
        emptyHint={inheritHint(
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
          effectiveValue={effective?.download_allowed}
        />
        <BooleanPolicyRow
          label="Download Transcodes"
          value={state.downloadTranscodeAllowed}
          onValueChange={(downloadTranscodeAllowed) =>
            onChange({ ...state, downloadTranscodeAllowed })
          }
          effectiveValue={effective?.download_transcode_allowed}
        />
      </div>
      <BooleanPolicyRow
        label="Media Requests"
        description="Request new movies and series when requests are enabled."
        value={state.requestsAllowed}
        onValueChange={(requestsAllowed) => onChange({ ...state, requestsAllowed })}
        effectiveValue={effective?.requests_allowed}
      />
    </>
  );
}

// Limits-tab policy fields: stream/transcode ceilings and the quality gate.
export function PolicyLimitFields({ state, onChange, effective }: PolicyContext) {
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
          effectiveValue={effective?.max_streams}
        />
        <LimitPolicyField
          label="Max Transcodes"
          value={state.maxTranscodes}
          onValueChange={(maxTranscodes) => onChange({ ...state, maxTranscodes })}
          effectiveValue={effective?.max_transcodes}
        />
      </div>
      <div className="grid gap-2 sm:grid-cols-2">
        <BooleanPolicyRow
          label="Video Transcoding"
          value={state.transcodeAllowed}
          onValueChange={(transcodeAllowed) => onChange({ ...state, transcodeAllowed })}
          effectiveValue={effective?.transcode_allowed}
        />
        <BooleanPolicyRow
          label="Audio Transcoding"
          description="Audio conversion without video encoding."
          value={state.audioTranscodeAllowed}
          onValueChange={(audioTranscodeAllowed) => onChange({ ...state, audioTranscodeAllowed })}
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
              {inheritHint(
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
            ? "Uses the access group's quality ceiling."
            : PLAYBACK_QUALITY_OPTIONS.find((option) => option.value === qualityValue)?.description}
        </p>
      </div>
    </>
  );
}
