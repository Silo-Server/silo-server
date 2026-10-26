// @vitest-environment node

import { describe, expect, it } from "vitest";

import { pluginRouteHref } from "./pluginRouteHref";

describe("pluginRouteHref", () => {
  it("only strips a trailing /* — not an embedded asterisk", () => {
    expect(pluginRouteHref(19, "/foo*bar")).toBe("/api/v2/plugin-content/plugins/19/foo*bar");
  });

  it("handles a bare /* descriptor by collapsing to the installation root", () => {
    expect(pluginRouteHref(19, "/*")).toBe("/api/v2/plugin-content/plugins/19");
  });
});
