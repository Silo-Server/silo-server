// @vitest-environment node

import { describe, expect, it } from "vitest";
import { parseVTT } from "./parseVTT";

describe("parseVTT", () => {
  it("parses standard cues with HH:MM:SS.mmm timestamps", () => {
    const vtt = `WEBVTT
Kind: captions
Language: en

1
00:00:01.000 --> 00:00:04.000
Hello, world!

2
00:00:05.000 --> 00:00:08.000
Second subtitle line.
`;
    const cues = parseVTT(vtt);
    expect(cues).toHaveLength(2);
    expect(cues[0]!).toEqual({ start: 1, end: 4, text: "Hello, world!" });
    expect(cues[1]!).toEqual({ start: 5, end: 8, text: "Second subtitle line." });
  });

  it("parses MM:SS.mmm timestamps", () => {
    const vtt = `WEBVTT

01:23.456 --> 02:34.789
Short format timestamp.
`;
    const cues = parseVTT(vtt);
    expect(cues).toHaveLength(1);
    expect(cues[0]!.start).toBeCloseTo(83.456, 3);
    expect(cues[0]!.end).toBeCloseTo(154.789, 3);
  });

  it("handles multi-line cue text", () => {
    const vtt = `WEBVTT

00:00:10.000 --> 00:00:15.000
Line one
Line two
Line three
`;
    const cues = parseVTT(vtt);
    expect(cues).toHaveLength(1);
    expect(cues[0]!.text).toBe("Line one\nLine two\nLine three");
  });

  it("returns empty array for empty input", () => {
    expect(parseVTT("")).toEqual([]);
    expect(parseVTT("WEBVTT\n\n")).toEqual([]);
  });

  it("handles Windows-style line endings", () => {
    const vtt = "WEBVTT\r\n\r\n00:00:01.000 --> 00:00:02.000\r\nHello!\r\n";
    const cues = parseVTT(vtt);
    expect(cues).toHaveLength(1);
    expect(cues[0]!.text).toBe("Hello!");
  });
});
