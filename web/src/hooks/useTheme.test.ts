import { describe, expect, it } from "vitest";
import { appearanceCacheOwner } from "./themePreferences";

describe("appearanceCacheOwner", () => {
  // No owner also means no API settings request: the hooks gate the query on
  // having resolved an owner.
  it("has no owner until auth bootstrap finishes", () => {
    expect(appearanceCacheOwner({ loading: true, user: null, profile: null })).toBeNull();
    expect(
      appearanceCacheOwner({ loading: true, user: { id: 1 }, profile: { id: "p1" } }),
    ).toBeNull();
  });

  it("has no owner when nobody is signed in", () => {
    expect(appearanceCacheOwner({ loading: false, user: null, profile: null })).toBeNull();
  });

  // Appearance is profile-scoped: on the profile picker there is no identity
  // to resolve settings for yet, so the cache keeps the device's last look.
  it("has no owner before a profile is selected", () => {
    expect(appearanceCacheOwner({ loading: false, user: { id: 7 }, profile: null })).toBeNull();
  });

  it("identifies the cache owner by user id plus active profile id", () => {
    expect(appearanceCacheOwner({ loading: false, user: { id: 7 }, profile: { id: "p2" } })).toBe(
      "7:p2",
    );
  });

  it("gives sibling profiles on one account distinct owners", () => {
    const user = { id: 7 };
    expect(appearanceCacheOwner({ loading: false, user, profile: { id: "p1" } })).not.toBe(
      appearanceCacheOwner({ loading: false, user, profile: { id: "p2" } }),
    );
  });
});
