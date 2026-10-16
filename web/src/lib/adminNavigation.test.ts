// @vitest-environment node

import { describe, expect, it } from "vitest";

import { filterSettingsSearchGroups } from "@/components/settings/settingsSearch";

import { ADMIN_SETTINGS_NAV } from "./adminSettingsSearch";
import {
  ADMIN_NAV_SECTIONS,
  buildAdminCommandNavSections,
  isAdminNavTopic,
} from "./adminNavigation";

const TOOL_PAGES = [
  "/admin",
  "/admin/activity",
  "/admin/logs",
  "/admin/diagnostics",
  "/admin/libraries",
  "/admin/autoscan",
  "/admin/autoscan/activity",
  "/admin/collections",
  "/admin/sections",
  "/admin/marker-history",
  "/admin/subtitles",
  "/admin/requests",
  "/admin/users",
  "/admin/access-groups",
  "/admin/devices",
  "/admin/history-import",
  "/admin/nodes",
  "/admin/history",
  "/admin/downloads",
  "/admin/tasks",
  "/admin/maintenance",
  "/admin/api-keys",
  "/admin/policy",
  "/admin/plugins",
  "/admin/recommendations",
];

describe("admin sidebar tree", () => {
  it("lists every admin page and settings page exactly once", () => {
    const hrefs = ADMIN_NAV_SECTIONS.flatMap((section) =>
      section.entries.flatMap((entry) =>
        isAdminNavTopic(entry) ? entry.items.map((item) => item.href) : [entry.href],
      ),
    );

    expect([...hrefs].sort()).toEqual(
      [...TOOL_PAGES, ...ADMIN_SETTINGS_NAV.map((page) => `/admin/settings/${page.id}`)].sort(),
    );
  });
});

function hrefsFor(query: string): string[] {
  return filterSettingsSearchGroups(buildAdminCommandNavSections(undefined), query).flatMap(
    (group) => group.items.map((item) => item.href),
  );
}

describe("admin command palette", () => {
  it.each(["sonarr", "radarr", "request servers", "routing", "anime", "quota", "request limit"])(
    "finds the Requests settings page for %s",
    (query) => {
      expect(hrefsFor(query)).toContain("/admin/settings/requests");
    },
  );

  it.each(["home rows", "sections", "rows", "rails"])("finds Sections for %s", (query) => {
    expect(hrefsFor(query)).toContain("/admin/sections");
  });

  it("still finds the request queue", () => {
    expect(hrefsFor("approvals")).toContain("/admin/requests");
  });

  it("groups a topic's pages under the topic, so a bare Settings says whose", () => {
    const requests = buildAdminCommandNavSections(undefined).find(
      (group) => group.label === "Requests",
    );

    expect(requests?.items.map((item) => [item.label, item.href])).toEqual([
      ["Queue", "/admin/requests"],
      ["Settings", "/admin/settings/requests"],
    ]);
  });
});
