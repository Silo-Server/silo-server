import { describe, expect, it, vi } from "vitest";
import type { EnginePlayer } from "./EnginePlayer";
import { attachEngineToElement } from "./facade";
import { EngineTextTrack } from "./textTracks";

function fakePlayer() {
  return {
    currentTime: 12,
    duration: 100,
    paused: true,
    ended: false,
    seeking: false,
    readyState: 4,
    buffered: { length: 0 },
    seekable: { length: 0 },
    volume: 0.5,
    muted: false,
    playbackRate: 1,
    videoWidth: 1920,
    videoHeight: 800,
    play: vi.fn(async () => {}),
    pause: vi.fn(),
    requestVideoFrameCallback: vi.fn(() => 7),
    cancelVideoFrameCallback: vi.fn(),
    addTextTrack: vi.fn(() => "track"),
  };
}

describe("attachEngineToElement", () => {
  it("forwards the media surface to the player and restores the element", async () => {
    const video = document.createElement("video");
    const player = fakePlayer();
    const detach = attachEngineToElement(video, player as unknown as EnginePlayer);

    expect(video.currentTime).toBe(12);
    expect(video.videoWidth).toBe(1920);
    expect(video.readyState).toBe(4);
    video.currentTime = 30;
    expect(player.currentTime).toBe(30);
    video.volume = 0.25;
    expect(player.volume).toBe(0.25);
    await video.play();
    expect(player.play).toHaveBeenCalled();
    expect(video.requestVideoFrameCallback(() => {})).toBe(7);
    expect(video.addTextTrack("subtitles")).toBe("track");
    expect(video.error).toBeNull();
    await expect(video.requestPictureInPicture()).rejects.toMatchObject({
      name: "NotSupportedError",
    });

    player.muted = true;
    detach();
    expect(Object.getOwnPropertyNames(video)).not.toContain("currentTime");
    expect(Object.getOwnPropertyNames(video)).not.toContain("play");
    expect(video.videoWidth).toBe(0);
    expect(video.volume).toBe(0.25);
    expect(video.muted).toBe(true);
  });
});

describe("EngineTextTrack", () => {
  it("activates cues against the engine time and reports changes once", () => {
    const track = new EngineTextTrack("subtitles", "Silo", "en");
    track.mode = "hidden";
    const first = { startTime: 1, endTime: 2 } as TextTrackCue;
    const second = { startTime: 1.5, endTime: 3 } as TextTrackCue;
    track.addCue(first);
    track.addCue(second);
    const changes = vi.fn();
    track.addEventListener("cuechange", changes);

    track.update(0.5);
    expect(changes).not.toHaveBeenCalled();
    track.update(1.6);
    expect(Array.from(track.activeCues!)).toEqual([first, second]);
    track.update(1.7);
    expect(changes).toHaveBeenCalledTimes(1);
    track.removeCue(first);
    track.update(1.8);
    expect(Array.from(track.activeCues!)).toEqual([second]);
    track.mode = "disabled";
    expect(track.cues).toBeNull();
  });
});
