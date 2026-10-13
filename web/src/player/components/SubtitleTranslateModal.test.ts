import { describe, expect, it } from "vitest";
import { buildSubtitleTranslateRequest, isTranslatableSource } from "./subtitleTranslateRequest";
import { sourceLabel, sourceLabels } from "./SubtitleTranslateModal";
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

// #1091: full, SDH and forced tracks in one language must read differently in
// the Translate from picker.
describe("sourceLabel", () => {
  it("distinguishes full, SDH and forced tracks by their flags", () => {
    const base = { language: "en", source: "embedded" as const, codec: "subrip" };
    expect(sourceLabel(track({ ...base, label: "English" }))).toBe("English · embedded");
    expect(sourceLabel(track({ ...base, label: "English", hearing_impaired: true }))).toBe(
      "English · SDH · embedded",
    );
    expect(sourceLabel(track({ ...base, label: "English", forced: true }))).toBe(
      "English · Forced · embedded",
    );
  });

  it("shows a descriptive title without repeating a flag it already names", () => {
    const base = { language: "en", source: "embedded" as const, codec: "subrip" };
    expect(sourceLabel(track({ ...base, label: "English SDH", hearing_impaired: true }))).toBe(
      "English · English SDH · embedded",
    );
    expect(sourceLabel(track({ ...base, label: "Forced", forced: true }))).toBe(
      "English · Forced · embedded",
    );
    expect(sourceLabel(track({ ...base, label: "Commentary" }))).toBe(
      "English · Commentary · embedded",
    );
  });

  it("still marks a flag the title negates", () => {
    const base = { language: "en", source: "embedded" as const, codec: "subrip" };
    expect(sourceLabel(track({ ...base, label: "Non-forced", forced: true }))).toBe(
      "English · Non-forced · Forced · embedded",
    );
    expect(sourceLabel(track({ ...base, label: "Not SDH", hearing_impaired: true }))).toBe(
      "English · Not SDH · SDH · embedded",
    );
  });

  it("drops a title that only repeats the format", () => {
    expect(sourceLabel(track({ language: "en", label: "SRT", codec: "subrip" }))).toBe("English");
  });

  it("numbers tracks that would otherwise read the same", () => {
    const labels = sourceLabels([
      track({ index: 2, label: "English", source: "embedded", codec: "subrip" }),
      track({ index: 5, label: "English", source: "embedded", codec: "subrip" }),
      track({ index: 6, label: "English", source: "embedded", codec: "subrip", forced: true }),
    ]);
    expect(labels.get(2)).toBe("English · embedded · track 3");
    expect(labels.get(5)).toBe("English · embedded · track 6");
    expect(labels.get(6)).toBe("English · Forced · embedded");
  });

  it("keeps a numbered label distinct from another option's label", () => {
    const labels = sourceLabels([
      track({ index: 2, label: "English", source: "embedded", codec: "subrip" }),
      track({ index: 5, label: "English", source: "embedded", codec: "subrip" }),
      track({ index: 9, label: "embedded · track 3", codec: "subrip" }),
    ]);
    expect(labels.get(9)).toBe("English · embedded · track 3");
    expect(labels.get(2)).toBe("English · embedded · track 3 (2)");
    expect(labels.get(5)).toBe("English · embedded · track 6");
    expect(new Set(labels.values()).size).toBe(3);
  });
});
