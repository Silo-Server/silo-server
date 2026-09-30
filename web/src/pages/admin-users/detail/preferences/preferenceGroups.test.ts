import { describe, expect, it } from "vitest";

import type { AdminUserSettingEntry } from "@/hooks/queries/admin/users";
import { getSettingDefinition } from "@/lib/settingsDisplay";
import { SETTING_KEYS } from "@/lib/settingsContract";

import { filterPreferences, groupPreferences } from "./preferenceGroups";

const names = {
  profiles: new Map([
    ["p-main", "Main"],
    ["p-kids", "Kids"],
  ]),
  libraries: new Map([[4, "Movies"]]),
};

function entry(overrides: Partial<AdminUserSettingEntry>): AdminUserSettingEntry {
  return {
    key: SETTING_KEYS.PLAYBACK_AUTO_SKIP_INTRO,
    scope: "account",
    value: "true",
    ...overrides,
  };
}

const entries: AdminUserSettingEntry[] = [
  entry({ scope: "profile_series", profile_id: "p-main", series_id: "series-9" }),
  entry({ scope: "profile_library", profile_id: "p-kids", library_id: 4 }),
  entry({ scope: "profile_client", profile_id: "p-main", client_family: "tv" }),
  entry({ scope: "profile", profile_id: "p-main" }),
  entry({ scope: "profile", profile_id: "p-kids", key: SETTING_KEYS.UI_SIDEBAR_PINS }),
  entry({ scope: "account" }),
  entry({ scope: "profile", profile_id: "p-gone" }),
];

describe("groupPreferences", () => {
  it("groups by where a setting applies, in scope order and in the account's profile order", () => {
    const groups = groupPreferences(entries, names);
    expect(groups.map((group) => [group.title, group.subtitle])).toEqual([
      ["Account-wide", "Applies to every profile"],
      ["Profile · Main", undefined],
      ["Profile · Kids", undefined],
      ["Profile · p-gone", undefined],
      ["Profile · Main · TV apps", undefined],
      ["Library · Movies", "Profile Kids"],
      ["Series · series-9", "Profile Main"],
    ]);
    expect(groups.every((group) => group.entries.length === 1)).toBe(true);
  });

  it("keeps rows for one profile together", () => {
    const groups = groupPreferences(
      [
        entry({ scope: "profile", profile_id: "p-main" }),
        entry({ scope: "profile", profile_id: "p-main", key: SETTING_KEYS.UI_SIDEBAR_PINS }),
      ],
      names,
    );
    expect(groups).toHaveLength(1);
    expect(groups[0]!.entries).toHaveLength(2);
  });

  it("falls back to ids for unknown libraries", () => {
    const [group] = groupPreferences(
      [entry({ scope: "profile_library", profile_id: "p-main", library_id: 12 })],
      names,
    );
    expect(group!.title).toBe("Library · #12");
  });
});

describe("filterPreferences", () => {
  const groups = groupPreferences(entries, names);

  it("filters by scope", () => {
    expect(filterPreferences(groups, "", "account").map((g) => g.title)).toEqual(["Account-wide"]);
    expect(filterPreferences(groups, "", "client").map((g) => g.title)).toEqual([
      "Profile · Main · TV apps",
    ]);
    expect(filterPreferences(groups, "", "library").map((g) => g.title)).toEqual([
      "Library · Movies",
    ]);
    expect(filterPreferences(groups, "", "series").map((g) => g.title)).toEqual([
      "Series · series-9",
    ]);
    expect(filterPreferences(groups, "", "profile")).toHaveLength(3);
    expect(filterPreferences(groups, "", "all")).toHaveLength(groups.length);
  });

  it("searches labels and keys and drops emptied groups", () => {
    const label = getSettingDefinition(SETTING_KEYS.UI_SIDEBAR_PINS)?.label ?? "";
    expect(label).not.toBe("");
    const byLabel = filterPreferences(groups, `  ${label.toUpperCase()} `, "all");
    expect(byLabel.map((g) => g.title)).toEqual(["Profile · Kids"]);
    const byKey = filterPreferences(groups, SETTING_KEYS.UI_SIDEBAR_PINS, "all");
    expect(byKey.map((g) => g.title)).toEqual(["Profile · Kids"]);
    expect(filterPreferences(groups, "no such setting", "all")).toEqual([]);
  });
});
