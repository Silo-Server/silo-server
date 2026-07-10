import { describe, expect, it } from "vitest";
import { buildStartPlaybackRequestPayload } from "./usePlaybackSession";

describe("buildStartPlaybackRequestPayload", () => {
  const baseInput = {
    targetFileId: 42,
    profileId: "profile-1",
    codecsVideo: ["h264"],
    codecsAudio: ["aac"],
    containers: ["mp4"],
    maxResolution: "2160p",
    hdr: true,
  };

  it("includes an explicit zero start position when forced", () => {
    expect(
      buildStartPlaybackRequestPayload({
        ...baseInput,
        position: 0,
        forceInitialPosition: true,
      }),
    ).toMatchObject({
      file_id: 42,
      profile_id: "profile-1",
      start_position: 0,
      supports_bitmap_subtitle_burn_in: true,
    });
  });

  it("omits zero start position when playback should resume normally", () => {
    expect(
      buildStartPlaybackRequestPayload({
        ...baseInput,
        position: 0,
        forceInitialPosition: false,
      }),
    ).not.toHaveProperty("start_position");
  });

  it("includes an explicit audio track override when present", () => {
    expect(
      buildStartPlaybackRequestPayload({
        ...baseInput,
        position: 0,
        forceInitialPosition: false,
        explicitAudioTrackIndex: 2,
      }),
    ).toMatchObject({
      file_id: 42,
      profile_id: "profile-1",
      audio_track_index: 2,
    });
  });
});
