import { describe, expect, it } from "vitest";
import { supportedMediaTypesForConfig } from "./requestIntegrationMediaTypes";

describe("supportedMediaTypesForConfig", () => {
  it("derives request media support from known service kinds", () => {
    expect(supportedMediaTypesForConfig({ service_kind: "radarr" })).toEqual(["movie"]);
    expect(supportedMediaTypesForConfig({ service_kind: "sonarr" })).toEqual(["series"]);
  });

  it("preserves the saved media support when the config has no known service kind", () => {
    const out = supportedMediaTypesForConfig(
      { service_kind: "custom" },
      {
        id: "conn-1",
        name: "Custom",
        enabled: true,
        base_url: "http://example.test",
        supported_media_types: ["movie"],
      },
    );

    expect(out).toEqual(["movie"]);
  });
});
