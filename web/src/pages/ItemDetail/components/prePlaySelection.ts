import type {
  DownloadedSubtitle,
  FileVersion,
  VersionAudioTrack,
  VersionSubtitleTrack,
} from "@/api/types";
import type {
  PlayerSubtitleInfo,
  PlayerSubtitleTrackSignature,
  PrePlaySubtitleSelection,
  SubtitleMode,
} from "@/player/types";
import { resolveVersionAudioLanguage } from "@/player/utils/effectiveAudioLanguage";
import { getLanguageName } from "@/player/utils/languageNames";
import { normalizeSubtitleMode } from "@/player/utils/subtitleMode";
import { resolveSubtitleAutoSelect } from "@/player/utils/subtitleSort";
import { getSubtitleFormatLabel } from "@/player/utils/subtitleCodecs";
import { formatAudioTrackLabel, formatChannels } from "@/lib/mediaFormat";
import {
  buildVersionSubtitleInventory,
  type VersionSubtitleInventoryRow,
} from "./versionSubtitleInventory";

export interface PrePlaySubtitleCandidate extends VersionSubtitleInventoryRow {
  selection: PrePlaySubtitleSelection;
  summary: string;
}

export interface PrePlaySubtitleCandidateSections {
  embedded: PrePlaySubtitleCandidate[];
  external: PrePlaySubtitleCandidate[];
  downloaded: PrePlaySubtitleCandidate[];
  all: PrePlaySubtitleCandidate[];
}

function normalizeTrackLabel(track: VersionSubtitleTrack, fallbackIndex: number): string {
  return (
    track.title?.trim() ||
    track.embedded_title?.trim() ||
    track.file_name?.trim() ||
    track.language?.trim() ||
    `Subtitle ${fallbackIndex + 1}`
  );
}

function normalizeDownloadedLabel(subtitle: DownloadedSubtitle): string {
  const releaseName = subtitle.release_name?.trim();
  const provider = subtitle.provider?.trim();
  if (releaseName && provider) {
    return `${releaseName} (${provider})`;
  }
  return releaseName || provider || getLanguageName(subtitle.language?.trim() || "unknown");
}

export function formatSubtitleCandidateSummary(
  row: Pick<VersionSubtitleInventoryRow, "languageLabel" | "forced" | "hearingImpaired">,
): string {
  return row.languageLabel;
}

export function inferSubtitleFlagsFromTitle(title: string | undefined): {
  forced: boolean;
  hearingImpaired: boolean;
  flagOnly: boolean;
} {
  const normalized = title?.trim().toLowerCase() ?? "";
  const forced = normalized === "forced";
  const hearingImpaired = ["sdh", "cc", "hi", "hearing impaired"].includes(normalized);
  return { forced, hearingImpaired, flagOnly: forced || hearingImpaired };
}

export interface SubtitlePillSummarySource {
  label?: string;
  languageLabel?: string;
  codec?: string;
  forced?: boolean;
  hearingImpaired?: boolean;
}

/**
 * Single-line summary for the closed selector pill: language, optional
 * (SDH)/(Forced) markers, and a human-readable subtitle format. Track titles
 * stay in the open selector, where meaningful details have room to display.
 */
export function formatSubtitlePillSummary(source: SubtitlePillSummarySource): string {
  const name = source.languageLabel?.trim() || source.label?.trim() || "Unknown";
  const parts = [name];
  if (source.hearingImpaired && !/\b(?:sdh|cc|hi)\b/i.test(name)) parts.push("(SDH)");
  if (source.forced && !/forced/i.test(name)) parts.push("(Forced)");
  const text = parts.join(" ");
  const codec = source.codec?.trim();
  const format = getSubtitleFormatLabel(codec) || codec?.toUpperCase();
  return format ? `${text} · ${format}` : text;
}

export function getAutoAudioTrackIndex(version: FileVersion | null | undefined): number {
  const tracks = version?.audio_tracks ?? [];
  if (tracks.length === 0) {
    return 0;
  }

  const effectiveIndex = version?.effective_audio_track_index;
  if (effectiveIndex != null && effectiveIndex >= 0 && effectiveIndex < tracks.length) {
    return effectiveIndex;
  }

  const defaultIndex = tracks.findIndex((track) => track.default);
  return defaultIndex >= 0 ? defaultIndex : 0;
}

export function resolveAudioTrackSelection(
  version: FileVersion | null | undefined,
  explicitAudioTrackIndex: number | null | undefined,
): {
  autoIndex: number;
  activeIndex: number;
  autoTrack: VersionAudioTrack | undefined;
  activeTrack: VersionAudioTrack | undefined;
} {
  const tracks = version?.audio_tracks ?? [];
  const autoIndex = getAutoAudioTrackIndex(version);
  const activeIndex =
    explicitAudioTrackIndex != null &&
    explicitAudioTrackIndex >= 0 &&
    explicitAudioTrackIndex < tracks.length
      ? explicitAudioTrackIndex
      : autoIndex;

  return {
    autoIndex,
    activeIndex,
    autoTrack: tracks[autoIndex],
    activeTrack: tracks[activeIndex],
  };
}

export function resolveSelectedAudioLanguage(
  version: FileVersion | null | undefined,
  explicitAudioTrackIndex: number | null | undefined,
): string | null {
  const { activeIndex } = resolveAudioTrackSelection(version, explicitAudioTrackIndex);
  return resolveVersionAudioLanguage(version, activeIndex);
}

export function formatAudioTrackSummary(track: VersionAudioTrack | undefined): string {
  if (!track) {
    return "Unknown";
  }

  const language = getLanguageName(track.language ?? "") || "Unknown";
  const codec = formatAudioTrackLabel(track);
  const channels = formatChannels(track.channels);
  return [language, codec, channels].filter(Boolean).join(" · ");
}

export function toSubtitleTrackSignature(
  selection: PrePlaySubtitleSelection | null | undefined,
): PlayerSubtitleTrackSignature | null {
  if (!selection) return null;
  return {
    source: selection.source,
    language: selection.language,
    codec: selection.codec,
    label: selection.label,
    forced: selection.forced,
    hearing_impaired: selection.hearing_impaired,
  };
}

export function subtitleSelectionEquals(
  left: PrePlaySubtitleSelection | null | undefined,
  right: PrePlaySubtitleSelection | null | undefined,
): boolean {
  if (!left || !right) return false;
  return (
    left.source === right.source &&
    (left.downloaded_subtitle_id ?? null) === (right.downloaded_subtitle_id ?? null) &&
    (left.external_subtitle_path ?? null) === (right.external_subtitle_path ?? null) &&
    (left.language ?? "") === (right.language ?? "") &&
    (left.codec ?? "") === (right.codec ?? "") &&
    (left.label ?? "") === (right.label ?? "") &&
    Boolean(left.forced) === Boolean(right.forced) &&
    Boolean(left.hearing_impaired) === Boolean(right.hearing_impaired)
  );
}

export function buildPrePlaySubtitleCandidates(
  tracks: VersionSubtitleTrack[] | undefined,
  downloaded: DownloadedSubtitle[] | undefined,
): PrePlaySubtitleCandidateSections {
  const inventory = buildVersionSubtitleInventory(tracks, downloaded);
  const all: PrePlaySubtitleCandidate[] = [];

  const mapBuiltIn = (rows: VersionSubtitleInventoryRow[], source: "embedded" | "external") =>
    rows.map((row, index) => {
      const track = (tracks ?? []).find(
        (candidate, candidateIndex) =>
          (candidate.external ? "external" : "embedded") === source &&
          (candidate.index ?? candidateIndex) === row.index,
      );
      const label = track ? normalizeTrackLabel(track, index) : row.title || row.languageLabel;
      const candidate: PrePlaySubtitleCandidate = {
        ...row,
        selection: {
          source,
          language: row.language,
          codec: row.codec,
          label,
          forced: row.forced,
          hearing_impaired: row.hearingImpaired,
          track_index: row.index,
        },
        summary: formatSubtitleCandidateSummary(row),
      };
      all.push(candidate);
      return candidate;
    });

  const embedded = mapBuiltIn(inventory.embedded, "embedded");
  const external = mapBuiltIn(inventory.external, "external");
  const downloadedRows = inventory.downloaded.map((row) => {
    const match = (downloaded ?? []).find((subtitle) => subtitle.id === row.downloadedSubtitleId);
    const label = match ? normalizeDownloadedLabel(match) : row.releaseName || row.languageLabel;
    const candidate: PrePlaySubtitleCandidate = {
      ...row,
      selection: {
        source: "downloaded",
        language: row.language,
        codec: row.codec,
        label,
        forced: row.forced,
        hearing_impaired: row.hearingImpaired,
        downloaded_subtitle_id: row.downloadedSubtitleId,
      },
      summary: formatSubtitleCandidateSummary(row),
    };
    all.push(candidate);
    return candidate;
  });

  return {
    embedded,
    external,
    downloaded: downloadedRows,
    all,
  };
}

export function resolveAutoSubtitleSelection(options: {
  candidates: PrePlaySubtitleCandidate[];
  preferredSubtitleLanguage?: string | null;
  preferredSubtitleTrackSignature?: PlayerSubtitleTrackSignature | null;
  subtitleMode?: SubtitleMode;
  showForcedSubtitles?: boolean;
  audioLanguage?: string | null;
  profileLanguage?: string | null;
}): PrePlaySubtitleCandidate | null {
  const {
    candidates,
    preferredSubtitleLanguage,
    preferredSubtitleTrackSignature,
    subtitleMode,
    showForcedSubtitles = true,
    audioLanguage,
    profileLanguage,
  } = options;

  if (candidates.length === 0) {
    return null;
  }

  const tracks: PlayerSubtitleInfo[] = candidates.map((candidate, index) => ({
    index,
    language: candidate.language,
    codec: candidate.codec,
    label: candidate.selection.label || candidate.languageLabel,
    source: candidate.source,
    forced: candidate.forced,
    hearing_impaired: candidate.hearingImpaired,
    url: "",
  }));

  const matchIndex = resolveSubtitleAutoSelect({
    mode: normalizeSubtitleMode(subtitleMode),
    tracks,
    preferredLanguage: preferredSubtitleLanguage ?? null,
    preferredTrackSignature: preferredSubtitleTrackSignature ?? null,
    audioLanguage: audioLanguage ?? null,
    profileLanguage: profileLanguage ?? null,
    showForcedSubtitles,
  });

  return matchIndex != null ? (candidates[matchIndex] ?? null) : null;
}
