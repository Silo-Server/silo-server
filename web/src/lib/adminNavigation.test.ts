// @vitest-environment node

import { describe, expect, it } from "vitest";

import { buildAdminCommandNavSections } from "./adminNavigation";

describe("admin command palette", () => {
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
