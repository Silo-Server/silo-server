import { describe, expect, it } from "vitest";

import type { PluginAdminFormField } from "@/api/types";

import { valueForField } from "./PluginConfigForm";

const multiSelectField: PluginAdminFormField = {
  key: "preferred_resolutions",
  label: "Preferred Resolutions",
  control: "MULTI_SELECT",
  required: true,
  secret: false,
  multiline: false,
  default_value: ["2160p", "1080p"],
};

describe("valueForField", () => {
  it("restores a saved multi-select array", () => {
    expect(
      valueForField(multiSelectField, {
        preferred_resolutions: ["2160p", "1440p", "1080p"],
      }),
    ).toEqual(["2160p", "1440p", "1080p"]);
  });

  it("uses a multi-select array default when no value was saved", () => {
    expect(valueForField(multiSelectField)).toEqual(["2160p", "1080p"]);
  });
});
