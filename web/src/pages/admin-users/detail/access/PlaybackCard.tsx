import { useId } from "react";

import type { AdminUser, UpdateUserRequest } from "@/api/types";
import type { PolicyInheritHints } from "@/components/UserPolicyFields";
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
import {
  PLAYBACK_QUALITY_OPTIONS,
  playbackQualityPresetFromValue,
  playbackQualityValueFromPreset,
} from "@/lib/playback-quality";

import { KeyValueRow } from "../ui";
import { EditableCard, type AccessCardProps } from "./EditableCard";
import { ChoicePolicyEdit, DefaultCustomSegment, PolicyValueRow } from "./PolicyRow";
import {
  countCustomRows,
  formatAllowed,
  formatBitrateCap,
  formatQuality,
  formatStreams,
  formatVideoTranscoding,
  inheritedValueText,
  parseWholeNumber,
  rowChanged,
  rowDraft,
  rowOverride,
  rowSource,
  sameVideoTranscoding,
  videoTranscodingFromEffective,
  videoTranscodingOverrides,
  type InheritContext,
  type PolicyRowKey,
  type RowDraft,
  type VideoTranscoding,
  ALLOWED_OPTIONS,
} from "./policySources";
import { useAccountCardDraft } from "./useAccountCardDraft";

const PLAYBACK_ROWS: PolicyRowKey[] = [
  "maxQuality",
  "maxStreams",
  "videoTranscoding",
  "audioTranscoding",
  "remoteBitrate",
  "localBitrate",
];

const LABELS = {
  quality: "Max quality",
  streams: "Simultaneous streams",
  video: "Video transcoding",
  audio: "Audio-only transcoding",
  remote: "Bitrate cap, remote",
  local: "Bitrate cap, local",
} as const;

const REMOTE_DESCRIPTION = "Streams from outside your network";

/** Where a Custom bitrate cap starts when the default is no cap. */
const CUSTOM_BITRATE_START_KBPS = 20000;

/** Video transcoding while edited: the mode and the typed count stay apart until valid. */
interface VideoDraft {
  custom: boolean;
  mode: VideoTranscoding["mode"] | null;
  maxText: string;
}

interface PlaybackDraft {
  quality: RowDraft<string>;
  streams: RowDraft<number>;
  video: VideoDraft;
  audio: RowDraft<boolean>;
  remote: RowDraft<number>;
  local: RowDraft<number>;
}

/** Quality overrides edit as the three presets the app offers. */
function canonicalQuality(value: string | null | undefined): string {
  return playbackQualityValueFromPreset(playbackQualityPresetFromValue(value));
}

function videoDraft(value: VideoTranscoding | null): VideoDraft {
  return {
    custom: value !== null,
    mode: value?.mode ?? null,
    maxText: value?.mode === "limit" ? String(value.max) : "",
  };
}

/** Null for Default, undefined while Custom is incomplete. */
function videoOverride(v: VideoDraft): VideoTranscoding | null | undefined {
  if (!v.custom) return null;
  if (v.mode === null) return undefined;
  if (v.mode !== "limit") return { mode: v.mode };
  const max = parseWholeNumber(v.maxText, 1);
  return max === null ? undefined : { mode: "limit", max };
}

function videoChanged(v: VideoDraft, saved: VideoTranscoding | null): boolean {
  const next = videoOverride(v);
  if (next === undefined) return saved !== null;
  return !sameVideoTranscoding(next, saved);
}

function toDraft(user: AdminUser): PlaybackDraft {
  // A custom row shows what applies now, which also covers an overridden
  // count under an inherited switch.
  const video =
    user.transcode_allowed === null && user.max_transcodes === null
      ? null
      : videoTranscodingFromEffective(
          user.effective_policy.transcode_allowed,
          user.effective_policy.max_transcodes,
        );
  return {
    quality: rowDraft(
      user.max_playback_quality === null ? null : canonicalQuality(user.max_playback_quality),
    ),
    streams: rowDraft(user.max_streams, user.max_streams === null ? "" : String(user.max_streams)),
    video: videoDraft(video),
    audio: rowDraft(user.audio_transcode_allowed),
    remote: rowDraft(user.max_remote_stream_bitrate_kbps),
    local: rowDraft(user.max_local_stream_bitrate_kbps),
  };
}

function savedVideo(base: PlaybackDraft): VideoTranscoding | null {
  return videoOverride(base.video) ?? null;
}

function changedRows(d: PlaybackDraft, base: PlaybackDraft): string[] {
  const rows: string[] = [];
  if (rowChanged(d.quality, rowOverride(base.quality) ?? null)) rows.push(LABELS.quality);
  if (rowChanged(d.streams, rowOverride(base.streams) ?? null)) rows.push(LABELS.streams);
  if (videoChanged(d.video, savedVideo(base))) rows.push(LABELS.video);
  if (rowChanged(d.audio, rowOverride(base.audio) ?? null)) rows.push(LABELS.audio);
  if (rowChanged(d.remote, rowOverride(base.remote) ?? null)) rows.push(LABELS.remote);
  if (rowChanged(d.local, rowOverride(base.local) ?? null)) rows.push(LABELS.local);
  return rows;
}

function toBody(d: PlaybackDraft, user: AdminUser): UpdateUserRequest {
  const base = toDraft(user);
  const body: UpdateUserRequest = {};
  const set = <T,>(row: RowDraft<T>, saved: RowDraft<T>, write: (value: T | null) => void) => {
    const next = rowOverride(row);
    if (next !== undefined && rowChanged(row, rowOverride(saved) ?? null)) write(next);
  };
  set(d.quality, base.quality, (value) => (body.max_playback_quality = value));
  set(d.streams, base.streams, (value) => (body.max_streams = value));
  set(d.audio, base.audio, (value) => (body.audio_transcode_allowed = value));
  set(d.remote, base.remote, (value) => (body.max_remote_stream_bitrate_kbps = value));
  set(d.local, base.local, (value) => (body.max_local_stream_bitrate_kbps = value));
  // The pair is sent only when this row changed, so an overridden count under
  // an inherited switch survives an unrelated save.
  const video = videoOverride(d.video);
  if (video !== undefined && videoChanged(d.video, savedVideo(base))) {
    Object.assign(body, videoTranscodingOverrides(video));
  }
  return body;
}

/** A row left Custom with nothing valid in place of a saved override can't be saved. */
function incomplete(d: PlaybackDraft, base: PlaybackDraft): boolean {
  const broken = <T,>(row: RowDraft<T>, saved: RowDraft<T>) =>
    rowOverride(row) === undefined && rowOverride(saved) !== null;
  return (
    broken(d.quality, base.quality) ||
    broken(d.streams, base.streams) ||
    broken(d.audio, base.audio) ||
    broken(d.remote, base.remote) ||
    broken(d.local, base.local) ||
    (videoOverride(d.video) === undefined && savedVideo(base) !== null)
  );
}

function inheritedVideo(hints: PolicyInheritHints): VideoTranscoding | undefined {
  if (hints.transcode_allowed === undefined || hints.max_transcodes === undefined) return undefined;
  return videoTranscodingFromEffective(hints.transcode_allowed, hints.max_transcodes);
}

function followsText(ctx: InheritContext): string {
  return ctx.kind === "group" ? ctx.name : "the server default";
}

const QUALITY_OPTIONS = PLAYBACK_QUALITY_OPTIONS.map((option) => ({
  value: playbackQualityValueFromPreset(option.value),
  label: formatQuality(playbackQualityValueFromPreset(option.value)),
}));

/** Keys quality options by preset: the "any" wire value is "", which Radix Select can't key. */
const qualityKey = (value: string) => playbackQualityPresetFromValue(value);

export function PlaybackCard({
  user,
  editor,
  manageable,
  available,
  libraries,
  ctx,
  hints,
}: AccessCardProps) {
  const draft = useAccountCardDraft({ id: "playback", editor, toDraft, toBody, changedRows });
  const streamsId = useId();
  const videoId = useId();
  const remoteId = useId();
  const localId = useId();
  const d = draft.draft;
  const saved = toDraft(draft.base ?? user);
  const effective = user.effective_policy;
  const custom = countCustomRows(user, PLAYBACK_ROWS);
  const changed = new Set(draft.changed);
  const setDraft = draft.setDraft;
  const inheritedStreams = hints.max_streams;
  const videoDefault = inheritedVideo(hints);

  const viewDescription =
    custom > 0
      ? `${custom} ${custom === 1 ? "limit" : "limits"} set for this account; the rest follow ${followsText(ctx)}.`
      : `Every limit follows ${followsText(ctx)}.`;
  const editDescription = (
    <>
      <strong className="text-foreground font-semibold">Default</strong> follows{" "}
      {ctx.kind === "group" ? `the ${ctx.name} group` : "the server default"}.{" "}
      <strong className="text-foreground font-semibold">Custom</strong> sets a value for this
      account only.
    </>
  );

  function bitrateRow(
    key: "remote" | "local",
    id: string,
    row: RowDraft<number>,
    inherited: number | undefined,
    description?: string,
  ) {
    const label = LABELS[key];
    return (
      <KeyValueRow
        label={row.custom ? <Label htmlFor={id}>{label}</Label> : label}
        description={description}
        changed={changed.has(label)}
        value={
          <DefaultCustomSegment
            label={label}
            defaultText={inherited === undefined ? undefined : formatBitrateCap(inherited)}
            custom={row.custom}
            onCustomChange={(on) =>
              setDraft((prev) => ({
                ...prev,
                // Custom starts from a real cap: the inherited one, or 20 Mbps
                // when the default is no cap (Unlimited would change nothing).
                [key]: on
                  ? { custom: true, value: inherited || CUSTOM_BITRATE_START_KBPS }
                  : { custom: false, value: null },
              }))
            }
          >
            <div className="w-full min-w-0 sm:w-auto sm:min-w-40">
              <StreamBitrateLimitInput
                key={draft.revision}
                id={id}
                label={label}
                value={row.value}
                onValueChange={(kbps) =>
                  setDraft((prev) => ({ ...prev, [key]: { custom: true, value: kbps } }))
                }
              />
            </div>
          </DefaultCustomSegment>
        }
      />
    );
  }

  return (
    <EditableCard
      id="playback"
      description={viewDescription}
      editDescription={editDescription}
      manageable={manageable}
      available={available}
      canEdit={editor !== undefined}
      invalid={d ? incomplete(d, saved) : false}
      state={draft}
    >
      {draft.editing && d ? (
        <>
          <ChoicePolicyEdit
            label={LABELS.quality}
            row={d.quality}
            saved={rowOverride(saved.quality) ?? null}
            inherited={
              hints.max_playback_quality === undefined
                ? undefined
                : canonicalQuality(hints.max_playback_quality)
            }
            options={QUALITY_OPTIONS}
            optionKey={qualityKey}
            onChange={(quality) => setDraft((prev) => ({ ...prev, quality }))}
          />
          <KeyValueRow
            label={
              d.streams.custom ? (
                <Label htmlFor={streamsId}>{LABELS.streams}</Label>
              ) : (
                LABELS.streams
              )
            }
            changed={changed.has(LABELS.streams)}
            value={
              <DefaultCustomSegment
                label={LABELS.streams}
                defaultText={
                  inheritedStreams === undefined ? undefined : formatStreams(inheritedStreams)
                }
                custom={d.streams.custom}
                onCustomChange={(on) =>
                  // Custom starts from what the account inherits; with that
                  // unknown the box starts empty and the row keeps its default.
                  setDraft((prev) => ({
                    ...prev,
                    streams: on
                      ? {
                          custom: true,
                          value: inheritedStreams ?? null,
                          text: inheritedStreams === undefined ? "" : String(inheritedStreams),
                        }
                      : { custom: false, value: null, text: "" },
                  }))
                }
              >
                <div className="flex flex-col items-end gap-1">
                  <Input
                    id={streamsId}
                    type="number"
                    inputMode="numeric"
                    min={0}
                    step={1}
                    className="w-24"
                    value={d.streams.text ?? ""}
                    aria-invalid={d.streams.value === null ? true : undefined}
                    onChange={(event) => {
                      const text = event.target.value;
                      setDraft((prev) => ({
                        ...prev,
                        streams: { custom: true, text, value: parseWholeNumber(text, 0) },
                      }));
                    }}
                  />
                  <span className="text-muted-foreground text-xs">
                    {d.streams.value === null
                      ? "Enter a whole number, or switch back to Default."
                      : "0 = unlimited"}
                  </span>
                </div>
              </DefaultCustomSegment>
            }
          />
          <KeyValueRow
            label={d.video.custom ? <Label htmlFor={videoId}>{LABELS.video}</Label> : LABELS.video}
            changed={changed.has(LABELS.video)}
            value={
              <DefaultCustomSegment
                label={LABELS.video}
                defaultText={videoDefault ? formatVideoTranscoding(videoDefault) : undefined}
                custom={d.video.custom}
                onCustomChange={(on) =>
                  setDraft((prev) => ({
                    ...prev,
                    video: videoDraft(on ? (videoDefault ?? null) : null),
                  }))
                }
              >
                <div className="flex items-center gap-2">
                  <Select
                    value={d.video.mode ?? ""}
                    onValueChange={(mode) =>
                      setDraft((prev) => ({
                        ...prev,
                        video: {
                          custom: true,
                          mode: mode as VideoTranscoding["mode"],
                          maxText:
                            mode === "limit" && prev.video.maxText === ""
                              ? "1"
                              : prev.video.maxText,
                        },
                      }))
                    }
                  >
                    <SelectTrigger id={videoId} className="w-32">
                      <SelectValue placeholder="Choose" />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="off">Off</SelectItem>
                      <SelectItem value="unlimited">Unlimited</SelectItem>
                      <SelectItem value="limit">Up to</SelectItem>
                    </SelectContent>
                  </Select>
                  {d.video.mode === "limit" ? (
                    <Input
                      type="number"
                      inputMode="numeric"
                      min={1}
                      step={1}
                      aria-label="Video transcodes at a time"
                      className="w-20"
                      value={d.video.maxText}
                      aria-invalid={
                        parseWholeNumber(d.video.maxText, 1) === null ? true : undefined
                      }
                      onChange={(event) => {
                        const maxText = event.target.value;
                        setDraft((prev) => ({ ...prev, video: { ...prev.video, maxText } }));
                      }}
                    />
                  ) : null}
                </div>
              </DefaultCustomSegment>
            }
          />
          <ChoicePolicyEdit
            label={LABELS.audio}
            row={d.audio}
            saved={rowOverride(saved.audio) ?? null}
            inherited={hints.audio_transcode_allowed}
            options={ALLOWED_OPTIONS}
            onChange={(audio) => setDraft((prev) => ({ ...prev, audio }))}
          />
          {bitrateRow(
            "remote",
            remoteId,
            d.remote,
            hints.max_remote_stream_bitrate_kbps,
            REMOTE_DESCRIPTION,
          )}
          {bitrateRow("local", localId, d.local, hints.max_local_stream_bitrate_kbps)}
        </>
      ) : (
        <>
          <PolicyValueRow
            label={LABELS.quality}
            value={formatQuality(effective.max_playback_quality)}
            source={rowSource(user, "maxQuality", ctx)}
            base={inheritedValueText("maxQuality", hints, ctx, libraries)}
          />
          <PolicyValueRow
            label={LABELS.streams}
            value={formatStreams(effective.max_streams)}
            source={rowSource(user, "maxStreams", ctx)}
            base={inheritedValueText("maxStreams", hints, ctx, libraries)}
          />
          <PolicyValueRow
            label={LABELS.video}
            value={formatVideoTranscoding(
              videoTranscodingFromEffective(effective.transcode_allowed, effective.max_transcodes),
            )}
            source={rowSource(user, "videoTranscoding", ctx)}
            base={inheritedValueText("videoTranscoding", hints, ctx, libraries)}
          />
          <PolicyValueRow
            label={LABELS.audio}
            value={formatAllowed(effective.audio_transcode_allowed)}
            source={rowSource(user, "audioTranscoding", ctx)}
            base={inheritedValueText("audioTranscoding", hints, ctx, libraries)}
          />
          <PolicyValueRow
            label={LABELS.remote}
            description={REMOTE_DESCRIPTION}
            value={formatBitrateCap(effective.max_remote_stream_bitrate_kbps)}
            source={rowSource(user, "remoteBitrate", ctx)}
            base={inheritedValueText("remoteBitrate", hints, ctx, libraries)}
          />
          <PolicyValueRow
            label={LABELS.local}
            value={formatBitrateCap(effective.max_local_stream_bitrate_kbps)}
            source={rowSource(user, "localBitrate", ctx)}
            base={inheritedValueText("localBitrate", hints, ctx, libraries)}
          />
        </>
      )}
    </EditableCard>
  );
}
