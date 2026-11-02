import { describe, expect, it } from "vitest";
import { boundFailureV3 } from "./playback-session-wire-v3";

describe("boundFailureV3", () => {
  it("cuts each field to the server's byte bound", () => {
    const failure = boundFailureV3({
      classification: "c".repeat(80),
      message: "m".repeat(600),
      decoder_name: "d".repeat(200),
    });
    expect(failure.classification).toHaveLength(64);
    expect(failure.message).toHaveLength(512);
    expect(failure.decoder_name).toHaveLength(128);
  });

  it("counts UTF-8 bytes and never splits a character", () => {
    const message = boundFailureV3({ classification: "x", message: "é".repeat(300) }).message!;
    expect(new TextEncoder().encode(message).length).toBeLessThanOrEqual(512);
    expect(message).toBe("é".repeat(256));
    const emoji = boundFailureV3({ classification: "x", message: "a" + "😀".repeat(200) }).message!;
    expect(new TextEncoder().encode(emoji).length).toBeLessThanOrEqual(512);
    expect(emoji.endsWith("\ud83d")).toBe(false);
  });

  it("leaves a failure within bounds as it is", () => {
    const failure = { classification: "decoder_error", message: "short" };
    expect(boundFailureV3(failure)).toEqual(failure);
  });
});
