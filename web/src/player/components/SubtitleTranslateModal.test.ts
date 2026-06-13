import { describe, expect, it } from "vitest";
import { buildSubtitleTranslateRequest, isTranslatableSource } from "./subtitleTranslateRequest";
import type { PlayerAudioTrack, PlayerSubtitleInfo } from "../types";

function track(p: Partial<PlayerSubtitleInfo>): PlayerSubtitleInfo {
  return { index: 0, language: "en", label: "", url: "", ...p };
}

function audioTrack(p: Partial<PlayerAudioTrack>): PlayerAudioTrack {
  return { language: "en", ...p };
}

describe("isTranslatableSource", () => {
  it("accepts text external/downloaded subtitles", () => {
    expect(isTranslatableSource(track({ source: "external", codec: "srt" }))).toBe(true);
    expect(isTranslatableSource(track({ source: "downloaded", codec: "subrip" }))).toBe(true);
    expect(isTranslatableSource(track({ source: "external", codec: "vtt" }))).toBe(true);
  });

  it("rejects ASS/SSA external/downloaded subtitles the server can't parse", () => {
    expect(isTranslatableSource(track({ source: "external", codec: "ass" }))).toBe(false);
    expect(isTranslatableSource(track({ source: "downloaded", codec: "ssa" }))).toBe(false);
  });

  it("accepts non-bitmap embedded tracks (extracted via ffmpeg, incl. ASS)", () => {
    expect(isTranslatableSource(track({ source: "embedded", codec: "ass" }))).toBe(true);
    expect(isTranslatableSource(track({ source: "embedded", codec: "subrip" }))).toBe(true);
  });

  it("rejects bitmap embedded tracks", () => {
    expect(isTranslatableSource(track({ source: "embedded", codec: "hdmv_pgs_subtitle" }))).toBe(
      false,
    );
    expect(isTranslatableSource(track({ source: "embedded", codec: "dvd_subtitle" }))).toBe(false);
  });

  it("rejects the in-progress live track", () => {
    expect(isTranslatableSource(track({ source: "downloaded", codec: "srt", live: true }))).toBe(
      false,
    );
  });
});

describe("buildSubtitleTranslateRequest", () => {
  it("normalizes 3-letter audio language codes before choosing the ASR job kind", () => {
    const body = buildSubtitleTranslateRequest({
      mode: "audio",
      mediaFileId: 42,
      audioIndex: 0,
      audioTracks: [audioTrack({ language: "eng" })],
      targetLang: "en",
      sessionId: "session-1",
      startPosition: 12.5,
    });

    expect(body).toMatchObject({
      media_file_id: 42,
      kind: "transcribe",
      source_index: 0,
      source_language: "en",
      target_language: "",
      session_id: "session-1",
      start_position: 12.5,
    });
  });
});
