// @vitest-environment node

import { describe, expect, it } from "vitest";

import { ADMIN_NAV_SECTIONS } from "./adminNavigation";
import { ADMIN_SETTINGS_NAV } from "./adminSettingsSearch";

// The sidebar is built from the registry while its module evaluates, so a
// settings page missing from ADMIN_SETTINGS_NAV throws during module load and
// the error boundary replaces every /admin route. A merge that keeps one side's
// nav and the other side's registry does that silently: the ids are strings, so
// tsc cannot see it. Walk the nav and name whatever the registry lacks.
describe("admin settings navigation", () => {
  it("links only to settings pages in the registry", () => {
    const registered = new Set(ADMIN_SETTINGS_NAV.map((page) => page.id));
    const linked = new Set<string>();
    for (const section of ADMIN_NAV_SECTIONS) {
      for (const entry of section.entries) {
        for (const item of "items" in entry ? entry.items : [entry]) {
          const match = /^\/admin\/settings\/([^/]+)$/.exec(item.href);
          if (match?.[1]) linked.add(decodeURIComponent(match[1]));
        }
      }
    }

    expect(linked.size).toBeGreaterThan(0);
    expect([...linked].filter((id) => !registered.has(id))).toEqual([]);
  });
});
