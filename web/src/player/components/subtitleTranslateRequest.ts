import { t, type MessageKey } from "@/i18n";
import type { PlayerAudioTrack, PlayerSubtitleInfo } from "../types";
import { getLanguageName, normalizeLanguageCode } from "../utils/languageNames";
import { isBitmapCodec, isSubtitleFormatLabel } from "../utils/subtitleCodecs";

// Formats the server can parse directly from an external/downloaded file.
const TRANSLATABLE_TEXT_CODECS = new Set(["srt", "subrip", "vtt", "webvtt"]);

// Mirror the server's loadSource acceptance: embedded non-bitmap tracks are
// extracted to text via ffmpeg, while external/downloaded sources must already
// be a parseable text format. Offering anything else starts a job that fails
// asynchronously instead of preventing the choice up front.
export function isTranslatableSource(track: PlayerSubtitleInfo): boolean {
  if (track.live) return false;
  const codec = (track.codec ?? "").toLowerCase();
  if (track.source === "embedded") {
    return !isBitmapCodec(codec);
  }
  return TRANSLATABLE_TEXT_CODECS.has(codec);
}

export type SubtitleTranslateMode = "subtitles" | "audio";

interface BuildSubtitleTranslateRequestInput {
  mode: SubtitleTranslateMode;
  mediaFileId: number;
  sourceTracks?: PlayerSubtitleInfo[];
  effectiveSourceIndex?: number | null;
  audioTracks?: PlayerAudioTrack[];
  audioIndex: number;
  targetLang: string;
  /** False when the server cannot translate: audio jobs stay in the spoken language. */
  translateTranscript?: boolean;
  sessionId?: string;
  startPosition: number;
}

interface SubtitleTranslateRequestBody {
  media_file_id: number;
  kind?: "transcribe" | "transcribe_translate";
  source_index: number;
  source_language: string;
  target_language: string;
  session_id: string;
  start_position: number;
}

export function buildSubtitleTranslateRequest(
  input: BuildSubtitleTranslateRequestInput,
): SubtitleTranslateRequestBody {
  const targetLanguage = normalizeLanguageCode(input.targetLang);
  if (input.mode === "audio") {
    const audio = input.audioTracks?.[input.audioIndex];
    const sourceLanguage = normalizeLanguageCode(audio?.language);
    const plain =
      input.translateTranscript === false ||
      (sourceLanguage !== "" && sourceLanguage === targetLanguage);
    return {
      media_file_id: input.mediaFileId,
      kind: plain ? "transcribe" : "transcribe_translate",
      source_index: input.audioIndex,
      source_language: sourceLanguage,
      target_language: plain ? "" : targetLanguage,
      session_id: input.sessionId ?? "",
      start_position: input.startPosition,
    };
  }

  const source = input.sourceTracks?.find((track) => track.index === input.effectiveSourceIndex);
  return {
    media_file_id: input.mediaFileId,
    source_index: input.effectiveSourceIndex ?? -1,
    source_language: normalizeLanguageCode(source?.language),
    target_language: targetLanguage,
    session_id: input.sessionId ?? "",
    start_position: input.startPosition,
  };
}

// titleNames reports whether a track title already states a flag, so the flag
// is not repeated. A negated mention ("Non-forced", "Not SDH") does not count.
function titleNames(title: string, words: string): boolean {
  return (
    new RegExp(`\\b(?:${words})\\b`, "i").test(title) &&
    !new RegExp(`\\b(?:non|not)[\\s-]*(?:${words})\\b`, "i").test(title)
  );
}

const SOURCE_NAMES: Record<string, MessageKey> = {
  embedded: "web.subtitles.translate.source.embedded",
  external: "web.subtitles.translate.source.external",
  downloaded: "web.subtitles.translate.source.downloaded",
};

// sourceLabel names a translation source so full, SDH and forced tracks in one
// language can be told apart: the track title when it says more than the
// language or format (as the subtitle menu shows it), then Forced and SDH when
// the title does not already say so, then where the track comes from.
export function sourceLabel(track: PlayerSubtitleInfo): string {
  const lang = getLanguageName(track.language) || track.language || t("web.languageName.unknown");
  const title = track.label?.trim() ?? "";
  const hasDetail =
    title !== "" &&
    title !== track.language &&
    title !== lang &&
    !isSubtitleFormatLabel(title, track.codec);
  const parts = [lang];
  if (hasDetail) parts.push(title);
  if (track.forced && !titleNames(title, "forced")) parts.push(t("web.subtitles.translate.forced"));
  if (track.hearing_impaired && !titleNames(title, "sdh|cc|hearing")) {
    parts.push(t("web.subtitles.translate.sdh"));
  }
  if (track.source) {
    const sourceName = SOURCE_NAMES[track.source];
    parts.push(sourceName ? t(sourceName) : track.source);
  }
  return parts.join(" · ");
}

// sourceLabels labels every source track so each option in the picker is
// distinct. Tracks whose labels would read the same get their track number,
// and a numbered label that still matches another option gets a counter.
export function sourceLabels(tracks: PlayerSubtitleInfo[]): Map<number, string> {
  const base = tracks.map((track) => ({ index: track.index, label: sourceLabel(track) }));
  const counts = new Map<string, number>();
  for (const { label } of base) counts.set(label, (counts.get(label) ?? 0) + 1);
  const used = new Set(
    base.filter(({ label }) => counts.get(label) === 1).map(({ label }) => label),
  );
  const labels = new Map<number, string>();
  for (const { index, label } of base) {
    if (counts.get(label) === 1) {
      labels.set(index, label);
      continue;
    }
    const numbered = t("web.subtitles.translate.trackNumbered", {
      label,
      number: index + 1,
    });
    let candidate = numbered;
    for (let n = 2; used.has(candidate); n++) {
      candidate = t("web.subtitles.translate.duplicate", { label: numbered, count: n });
    }
    used.add(candidate);
    labels.set(index, candidate);
  }
  return labels;
}

/** An audio track's name in the AI dialog's Audio track dropdown. */
export function audioLabel(track: PlayerAudioTrack, i: number): string {
  const lang =
    getLanguageName(track.language ?? "") ||
    track.language ||
    t("web.subtitles.translate.audioTrackNumber", { number: i + 1 });
  const parts = [lang];
  if (track.layout) parts.push(track.layout);
  if (track.default) parts.push(t("web.subtitles.translate.audioDefault"));
  return parts.join(" · ");
}
