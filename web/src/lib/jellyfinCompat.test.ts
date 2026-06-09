import { describe, expect, it } from "vitest";

import { hasPinnedJellyfinWebInstalled, normalizeJellyfinCompatVersion } from "./jellyfinCompat";

type JellyfinWebInstallStatus = Parameters<typeof hasPinnedJellyfinWebInstalled>[0];

function status(overrides: Partial<NonNullable<JellyfinWebInstallStatus>> = {}) {
  return {
    pinned_version: "10.11.6",
    installed_version: "10.11.6",
    ...overrides,
  } satisfies NonNullable<JellyfinWebInstallStatus>;
}

describe("jellyfinCompat helpers", () => {
  it("normalizes leading v prefixes", () => {
    expect(normalizeJellyfinCompatVersion(" v10.11.6 ")).toBe("10.11.6");
    expect(normalizeJellyfinCompatVersion("V10.11.6")).toBe("10.11.6");
  });

  it("detects when the installed Web UI matches the pinned version", () => {
    expect(hasPinnedJellyfinWebInstalled(status())).toBe(true);
    expect(hasPinnedJellyfinWebInstalled(status({ installed_version: "v10.11.6" }))).toBe(true);
  });

  it("does not treat missing or outdated installs as the pinned version", () => {
    expect(hasPinnedJellyfinWebInstalled(status({ installed_version: "" }))).toBe(false);
    expect(hasPinnedJellyfinWebInstalled(status({ installed_version: "10.11.5" }))).toBe(false);
    expect(hasPinnedJellyfinWebInstalled(null)).toBe(false);
  });
});
