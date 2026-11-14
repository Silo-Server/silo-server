import { useSyncExternalStore } from "react";
import type { FileVersion } from "@/api/types";
import type { PlayerAudioTrack, PlayerSubtitleInfo } from "./types";

/** The last inventory the player actually folded for one file. */
export interface PlaybackInventorySnapshot {
  fileId: number;
  audioTracks: PlayerAudioTrack[];
  subtitleTracks: PlayerSubtitleInfo[];
  /**
   * Whether each list is probe evidence for this file. A declared list is the
   * release listing the player started from; it is shown immediately but must
   * never replace a probed list the item page already has.
   */
  audioVerified: boolean;
  subtitleVerified: boolean;
  updatedAt: number;
}

const snapshots = new Map<number, PlaybackInventorySnapshot>();
const listeners = new Set<() => void>();

function emit() {
  for (const listener of listeners) listener();
}

/**
 * Records the player's audio/subtitle inventory for a file.
 * The item page reads this after playback so its menus don't fall back to a
 * stale catalog row while the player's probe evidence is newer.
 */
export function publishPlaybackInventory(
  fileId: number | null | undefined,
  audioTracks: readonly PlayerAudioTrack[] | null | undefined,
  subtitleTracks: readonly PlayerSubtitleInfo[] | null | undefined,
  provenance: { audioVerified?: boolean; subtitleVerified?: boolean } = {},
) {
  if (!fileId || fileId <= 0) return;
  const audio = (audioTracks ?? []).map((track) => ({ ...track }));
  const subtitles = (subtitleTracks ?? []).map((track) => ({ ...track }));
  const audioVerified = provenance.audioVerified === true;
  const subtitleVerified = provenance.subtitleVerified === true;
  const previous = snapshots.get(fileId);
  if (
    previous &&
    previous.audioVerified === audioVerified &&
    previous.subtitleVerified === subtitleVerified &&
    previous.audioTracks.length === audio.length &&
    previous.subtitleTracks.length === subtitles.length &&
    JSON.stringify(previous.audioTracks) === JSON.stringify(audio) &&
    JSON.stringify(previous.subtitleTracks) === JSON.stringify(subtitles)
  ) {
    return;
  }
  snapshots.set(fileId, {
    fileId,
    audioTracks: audio,
    subtitleTracks: subtitles,
    audioVerified,
    subtitleVerified,
    updatedAt: Date.now(),
  });
  emit();
}

function subscribe(listener: () => void) {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

function getSnapshot(fileId: number | null | undefined) {
  return fileId ? (snapshots.get(fileId) ?? null) : null;
}

/** Reads the player's last inventory for a file, if it has played. */
export function usePlaybackInventoryMirror(fileId: number | null | undefined) {
  return useSyncExternalStore(
    subscribe,
    () => getSnapshot(fileId),
    () => null,
  );
}

/**
 * Whether a plan's inventory also describes the row the viewer asked for.
 *
 * The player publishes the plan's inventory under the effective file id, which
 * is what the transport actually serves. Re-publishing it under the requested
 * id as well is only valid while the plan is about that row. A virtual
 * rotation does not move `effective_media_file_id` — the contract keeps it at
 * the requested catalog row and moves `virtual_source_revision` instead — so
 * the two ids differ exactly when the server served a different row: a
 * substitution, or a lower version chosen for this device. That release's audio
 * and subtitle tables say nothing about the requested version, and the catalog
 * row for it keeps its own inventory (or reads as undiscovered).
 */
export function planInventoryDescribesRequestedFile(
  plan:
    | {
        requested_media_file_id?: number | null;
        effective_media_file_id?: number | null;
      }
    | null
    | undefined,
): boolean {
  if (!plan) return false;
  const requested = plan.requested_media_file_id ?? 0;
  const effective = plan.effective_media_file_id ?? 0;
  return requested > 0 && requested === effective;
}

/**
 * The file ids a plan's inventory belongs under.
 *
 * The effective row always gets it: that is the file the transport serves. The
 * requested row gets it too only while the plan still serves that row, so a
 * substituted release's tracks never surface on the version the viewer picked.
 */
export function inventoryPublicationTargets(
  plan:
    | {
        requested_media_file_id?: number | null;
        effective_media_file_id?: number | null;
      }
    | null
    | undefined,
  effectiveFileId: number | null | undefined,
): number[] {
  if (!effectiveFileId || effectiveFileId <= 0) return [];
  const requested = plan?.requested_media_file_id ?? null;
  if (requested && requested !== effectiveFileId && planInventoryDescribesRequestedFile(plan)) {
    return [effectiveFileId, requested];
  }
  return [effectiveFileId];
}

interface MergePlaybackInventoryOptions {
  /** The version's audio list is already probe evidence for this file. */
  audioProbed?: boolean;
  /** The version's subtitle list is already probe evidence for this file. */
  subtitlesProbed?: boolean;
}

/**
 * Overlays a player inventory snapshot on a catalog/watch-detail version.
 *
 * A declared snapshot is the player's release listing, not evidence: it may
 * refine another declared list, but it never replaces a probed list. A verified
 * snapshot replaces both, including a verified empty subtitle inventory.
 */
export function mergePlaybackInventory(
  version: FileVersion,
  snapshot: PlaybackInventorySnapshot | null | undefined,
  options: MergePlaybackInventoryOptions = {},
): FileVersion {
  if (!snapshot) return version;
  const keepProbedAudio =
    options.audioProbed === true && !snapshot.audioVerified && snapshot.audioTracks.length > 0;
  const keepProbedSubtitles =
    options.subtitlesProbed === true &&
    !snapshot.subtitleVerified &&
    snapshot.subtitleTracks.length > 0;
  return {
    ...version,
    audio_tracks:
      snapshot.audioTracks.length > 0 && !keepProbedAudio
        ? snapshot.audioTracks.map((track) => ({
            title: track.title,
            embedded_title: track.embedded_title,
            language: track.language,
            languages: track.languages,
            codec: track.codec,
            profile: track.profile,
            layout: track.layout,
            channels: track.channels,
            bitrate: track.bitrate,
            sample_rate: track.sample_rate,
            bit_depth: track.bit_depth,
            default: track.default,
          }))
        : snapshot.audioVerified && !options.audioProbed
          ? []
          : version.audio_tracks,
    subtitle_tracks:
      snapshot.subtitleTracks.length > 0 && !keepProbedSubtitles
        ? snapshot.subtitleTracks.map((track) => ({
            index: track.index,
            language: track.language,
            codec: track.codec,
            title: track.label,
            embedded_title: track.label,
            forced: track.forced,
            hearing_impaired: track.hearing_impaired,
            default: track.default,
            external: track.source === "external",
          }))
        : snapshot.subtitleVerified && !options.subtitlesProbed
          ? []
          : version.subtitle_tracks,
  };
}
