import { describe, expect, it } from "vitest";
import type { PluginAdminForm } from "@/api/types";
import { evaluateShowWhen, validateSchemaValues, buildSchemaValues } from "./schemaForm";

const descriptor: PluginAdminForm = {
  fields: [
    { key: "service_kind", label: "Service", control: "SELECT", required: true, secret: false, multiline: false,
      options: [{ value: "radarr", label: "Radarr" }, { value: "sonarr", label: "Sonarr" }] },
    { key: "quality_profile_id", label: "QP", control: "SELECT", required: true, secret: false, multiline: false, dynamic_options: true },
    { key: "tags", label: "Tags", control: "MULTI_SELECT", required: false, secret: false, multiline: false, dynamic_options: true },
    { key: "season_folder", label: "Season folder", control: "SWITCH", required: false, secret: false, multiline: false,
      show_when: [{ field: "service_kind", equals: ["sonarr"] }] },
  ],
};

describe("evaluateShowWhen", () => {
  it("shows when all conditions match (stringified)", () => {
    expect(evaluateShowWhen([{ field: "service_kind", equals: ["sonarr"] }], { service_kind: "sonarr" })).toBe(true);
    expect(evaluateShowWhen([{ field: "service_kind", equals: ["sonarr"] }], { service_kind: "radarr" })).toBe(false);
  });
  it("matches booleans by stringified value", () => {
    expect(evaluateShowWhen([{ field: "anime_enabled", equals: ["true"] }], { anime_enabled: true })).toBe(true);
    expect(evaluateShowWhen([{ field: "anime_enabled", equals: ["true"] }], { anime_enabled: false })).toBe(false);
  });
  it("empty conditions => always visible", () => {
    expect(evaluateShowWhen(undefined, {})).toBe(true);
  });
});

describe("validateSchemaValues", () => {
  it("flags required visible fields that are empty", () => {
    const errs = validateSchemaValues(descriptor, { service_kind: "radarr" });
    expect(errs.quality_profile_id).toMatch(/required/i);
    expect(errs.service_kind).toBeUndefined();
  });
  it("ignores required fields hidden by show_when", () => {
    const d: PluginAdminForm = { fields: [{ key: "x", label: "X", control: "TEXT", required: true, secret: false, multiline: false, show_when: [{ field: "k", equals: ["yes"] }] }] };
    expect(validateSchemaValues(d, { k: "no" })).toEqual({});
  });
});

describe("buildSchemaValues", () => {
  it("coerces multi-select to an array and numbers to numbers", () => {
    const out = buildSchemaValues(descriptor, { service_kind: "sonarr", quality_profile_id: "3", tags: ["1", "2"], season_folder: true });
    expect(out.quality_profile_id).toBe(3);
    expect(out.tags).toEqual([1, 2]);
    expect(out.season_folder).toBe(true);
  });
});

describe("buildSchemaValues hidden fields", () => {
  it("drops a field hidden by show_when even if its draft value is set", () => {
    const d: PluginAdminForm = {
      fields: [
        { key: "is_4k", label: "4K", control: "SWITCH", required: false, secret: false, multiline: false },
        { key: "is_default_4k", label: "Default 4K", control: "SWITCH", required: false, secret: false, multiline: false,
          show_when: [{ field: "is_4k", equals: ["true"] }] },
      ],
    };
    const out = buildSchemaValues(d, { is_4k: false, is_default_4k: true });
    expect(out.is_default_4k).toBeUndefined(); // hidden -> not persisted
    expect(out.is_4k).toBe(false);
  });
  it("keeps a field when its show_when is satisfied", () => {
    const d: PluginAdminForm = {
      fields: [
        { key: "is_4k", label: "4K", control: "SWITCH", required: false, secret: false, multiline: false },
        { key: "is_default_4k", label: "Default 4K", control: "SWITCH", required: false, secret: false, multiline: false,
          show_when: [{ field: "is_4k", equals: ["true"] }] },
      ],
    };
    const out = buildSchemaValues(d, { is_4k: true, is_default_4k: true });
    expect(out.is_default_4k).toBe(true);
  });
});
