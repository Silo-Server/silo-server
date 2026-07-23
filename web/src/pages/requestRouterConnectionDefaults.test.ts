import { describe, expect, it } from "vitest";

import { requestRouterConnectionDefaults } from "./requestRouterConnectionDefaults";

describe("requestRouterConnectionDefaults", () => {
  it("reads complete plugin-managed defaults", () => {
    expect(
      requestRouterConnectionDefaults({
        connection_defaults: {
          base_url: "plugin://virtual-library",
          api_key: "plugin-managed",
        },
      }),
    ).toEqual({ baseURL: "plugin://virtual-library", apiKey: "plugin-managed" });
  });

  it("does not partially populate credentials", () => {
    expect(requestRouterConnectionDefaults(undefined)).toBeNull();
    expect(requestRouterConnectionDefaults({ connection_defaults: { base_url: "x" } })).toBeNull();
  });
});
