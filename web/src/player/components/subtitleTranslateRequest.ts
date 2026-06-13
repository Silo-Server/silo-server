import type { PlayerAudioTrack, PlayerSubtitleInfo } from "../types";
import { normalizeLanguageCode } from "../utils/languageNames";
import { isBitmapCodec } from "../utils/subtitleCodecs";

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
    const sameLanguage = sourceLanguage !== "" && sourceLanguage === targetLanguage;
    return {
      media_file_id: input.mediaFileId,
      kind: sameLanguage ? "transcribe" : "transcribe_translate",
      source_index: input.audioIndex,
      source_language: sourceLanguage,
      target_language: sameLanguage ? "" : targetLanguage,
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
