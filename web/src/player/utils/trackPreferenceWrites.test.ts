import { describe, expect, it } from "vitest";
import type { PlayerConfig } from "../context/PlayerConfigContext";
import {
  runPlayerTrackPreferenceWrite,
  StaleTrackPreferenceWriteError,
} from "./trackPreferenceWrites";

function held() {
  let release!: () => void;
  const promise = new Promise<void>((resolve) => (release = resolve));
  return { promise, release };
}

function playerConfig(profile: { id: string }): PlayerConfig {
  return {
    apiBaseUrl: "/api/v2",
    getAccessToken: () => "token",
    getProfileId: () => profile.id,
    getDeviceId: () => "device",
  };
}

describe("runPlayerTrackPreferenceWrite", () => {
  it("drops a queued write when the profile that chose it is no longer active", async () => {
    const profile = { id: "profile-a" };
    const config = playerConfig(profile);
    const slow = held();
    const first = runPlayerTrackPreferenceWrite(config, "audio", "series-1", () => slow.promise);
    const sentAs: (string | null)[] = [];
    const queued = runPlayerTrackPreferenceWrite(config, "audio", "series-1", async (owner) => {
      sentAs.push(owner.getProfileId());
    });

    const outcome = queued.catch((error: unknown) => error);
    // Let the first write start under profile A before the switch.
    await new Promise((resolve) => setTimeout(resolve, 0));

    profile.id = "profile-b";
    slow.release();
    await first;

    expect(await outcome).toBeInstanceOf(StaleTrackPreferenceWriteError);
    expect(sentAs).toEqual([]);
  });

  it("sends a queued write as the profile that chose it", async () => {
    const config = playerConfig({ id: "profile-a" });
    const slow = held();
    const first = runPlayerTrackPreferenceWrite(config, "subtitle", "series-1", () => slow.promise);
    const queued = runPlayerTrackPreferenceWrite(config, "subtitle", "series-1", async (owner) =>
      owner.getProfileId(),
    );

    slow.release();
    await first;

    await expect(queued).resolves.toBe("profile-a");
  });
});
