import { describe, expect, it } from "vitest";
import type { PluginAdminForm, PluginAdminFormField } from "@/api/types";
import {
  evaluateShowWhen,
  validateSchemaValues,
  buildSchemaValues,
  parseFieldTypes,
  coerceFieldValue,
} from "./schemaFormUtils";

const descriptor: PluginAdminForm = {
  fields: [
    {
      key: "service_kind",
      label: "Service",
      control: "SELECT",
      required: true,
      secret: false,
      multiline: false,
      options: [
        { value: "radarr", label: "Radarr" },
        { value: "sonarr", label: "Sonarr" },
      ],
    },
    {
      key: "quality_profile_id",
      label: "QP",
      control: "SELECT",
      required: true,
      secret: false,
      multiline: false,
      dynamic_options: true,
    },
    {
      key: "tags",
      label: "Tags",
      control: "MULTI_SELECT",
      required: false,
      secret: false,
      multiline: false,
      dynamic_options: true,
    },
    {
      key: "season_folder",
      label: "Season folder",
      control: "SWITCH",
      required: false,
      secret: false,
      multiline: false,
      show_when: [{ field: "service_kind", equals: ["sonarr"] }],
    },
  ],
};

describe("evaluateShowWhen", () => {
  it("shows when all conditions match (stringified)", () => {
    expect(
      evaluateShowWhen([{ field: "service_kind", equals: ["sonarr"] }], { service_kind: "sonarr" }),
    ).toBe(true);
    expect(
      evaluateShowWhen([{ field: "service_kind", equals: ["sonarr"] }], { service_kind: "radarr" }),
    ).toBe(false);
  });
  it("matches booleans by stringified value", () => {
    expect(
      evaluateShowWhen([{ field: "anime_enabled", equals: ["true"] }], { anime_enabled: true }),
    ).toBe(true);
    expect(
      evaluateShowWhen([{ field: "anime_enabled", equals: ["true"] }], { anime_enabled: false }),
    ).toBe(false);
  });
  it("empty conditions => always visible", () => {
    expect(evaluateShowWhen(undefined, {})).toBe(true);
  });
  it("falls back to the controlling field default", () => {
    const fields: PluginAdminFormField[] = [
      {
        key: "anime_enabled",
        label: "Anime",
        control: "SWITCH",
        required: false,
        secret: false,
        multiline: false,
        default_value: true,
      },
    ];
    expect(evaluateShowWhen([{ field: "anime_enabled", equals: ["true"] }], {}, fields)).toBe(true);
  });
});

describe("validateSchemaValues", () => {
  it("flags required visible fields that are empty", () => {
    const errs = validateSchemaValues(descriptor, { service_kind: "radarr" });
    expect(errs.quality_profile_id).toMatch(/required/i);
    expect(errs.service_kind).toBeUndefined();
  });
  it("ignores required fields hidden by show_when", () => {
    const d: PluginAdminForm = {
      fields: [
        {
          key: "x",
          label: "X",
          control: "TEXT",
          required: true,
          secret: false,
          multiline: false,
          show_when: [{ field: "k", equals: ["yes"] }],
        },
      ],
    };
    expect(validateSchemaValues(d, { k: "no" })).toEqual({});
  });
});

describe("buildSchemaValues", () => {
  it("coerces multi-select to an array and numbers to numbers", () => {
    const out = buildSchemaValues(descriptor, {
      service_kind: "sonarr",
      quality_profile_id: "3",
      tags: ["1", "2"],
      season_folder: true,
    });
    expect(out.quality_profile_id).toBe(3);
    expect(out.tags).toEqual([1, 2]);
    expect(out.season_folder).toBe(true);
  });
});

describe("validateSchemaValues regex guard (#5)", () => {
  it("does not throw on a malformed pattern; treats it as no constraint", () => {
    const d: PluginAdminForm = {
      fields: [
        {
          key: "name",
          label: "Name",
          control: "TEXT",
          required: false,
          secret: false,
          multiline: false,
          validation: { pattern: "[" },
        },
      ],
    };
    expect(() => validateSchemaValues(d, { name: "anything" })).not.toThrow();
    expect(validateSchemaValues(d, { name: "anything" })).toEqual({});
  });
  it("still flags a valid pattern mismatch", () => {
    const d: PluginAdminForm = {
      fields: [
        {
          key: "name",
          label: "Name",
          control: "TEXT",
          required: false,
          secret: false,
          multiline: false,
          validation: { pattern: "^[a-z]+$" },
        },
      ],
    };
    expect(validateSchemaValues(d, { name: "ABC" }).name).toMatch(/invalid/i);
    expect(validateSchemaValues(d, { name: "abc" })).toEqual({});
  });
});

describe("parseFieldTypes (#15)", () => {
  it("reads declared property types including array item types", () => {
    const json = JSON.stringify({
      type: "object",
      properties: {
        quality_profile_id: { type: "integer" },
        root_folder: { type: "string" },
        tags: { type: "array", items: { type: "integer" } },
        labels: { type: "array", items: { type: "string" } },
        enabled: { type: "boolean" },
      },
    });
    expect(parseFieldTypes(json)).toEqual({
      quality_profile_id: "integer",
      root_folder: "string",
      tags: "array:int",
      labels: "array",
      enabled: "boolean",
    });
  });
  it("returns {} for invalid or empty json_schema", () => {
    expect(parseFieldTypes("")).toEqual({});
    expect(parseFieldTypes("not json")).toEqual({});
    expect(parseFieldTypes(undefined)).toEqual({});
    expect(parseFieldTypes("[]")).toEqual({});
  });
});

describe("buildSchemaValues type-driven coercion (#15)", () => {
  it("coerces by declared type, preserving string ids and numeric array items", () => {
    const d: PluginAdminForm = {
      fields: [
        {
          key: "quality_profile_id",
          label: "QP",
          control: "SELECT",
          required: false,
          secret: false,
          multiline: false,
          dynamic_options: true,
        },
        {
          key: "root_folder",
          label: "Root",
          control: "SELECT",
          required: false,
          secret: false,
          multiline: false,
          dynamic_options: true,
        },
        {
          key: "tags",
          label: "Tags",
          control: "MULTI_SELECT",
          required: false,
          secret: false,
          multiline: false,
          dynamic_options: true,
        },
      ],
    };
    const out = buildSchemaValues(
      d,
      { quality_profile_id: "3", root_folder: "007", tags: ["1", "2"] },
      { quality_profile_id: "integer", root_folder: "string", tags: "array:int" },
    );
    expect(out.quality_profile_id).toBe(3);
    expect(out.root_folder).toBe("007"); // string preserved, NOT 7
    expect(out.tags).toEqual([1, 2]);
  });
});

describe("buildSchemaValues default_value (#6)", () => {
  it("persists an untouched declared default", () => {
    const d: PluginAdminForm = {
      fields: [
        {
          key: "season_folder",
          label: "Season folder",
          control: "SWITCH",
          required: false,
          secret: false,
          multiline: false,
          default_value: true,
        },
      ],
    };
    const out = buildSchemaValues(d, {});
    expect(out.season_folder).toBe(true);
  });
  it("required field satisfied by its default value", () => {
    const d: PluginAdminForm = {
      fields: [
        {
          key: "season_folder",
          label: "Season folder",
          control: "SWITCH",
          required: true,
          secret: false,
          multiline: false,
          default_value: true,
        },
      ],
    };
    expect(validateSchemaValues(d, {})).toEqual({});
  });
  it("uses a controller default when validating a conditional required field", () => {
    const d: PluginAdminForm = {
      fields: [
        {
          key: "anime_enabled",
          label: "Anime",
          control: "SWITCH",
          required: false,
          secret: false,
          multiline: false,
          default_value: true,
        },
        {
          key: "anime_root_folder",
          label: "Anime root folder",
          control: "TEXT",
          required: true,
          secret: false,
          multiline: false,
          show_when: [{ field: "anime_enabled", equals: ["true"] }],
        },
      ],
    };
    expect(validateSchemaValues(d, {}).anime_root_folder).toMatch(/required/i);
  });
  it("uses a controller default before dropping hidden fields", () => {
    const d: PluginAdminForm = {
      fields: [
        {
          key: "season_folder",
          label: "Season folder",
          control: "SWITCH",
          required: false,
          secret: false,
          multiline: false,
          default_value: true,
        },
        {
          key: "series_root_folder",
          label: "Series root folder",
          control: "TEXT",
          required: false,
          secret: false,
          multiline: false,
          default_value: "/tv",
          show_when: [{ field: "season_folder", equals: ["true"] }],
        },
      ],
    };
    expect(buildSchemaValues(d, {}).series_root_folder).toBe("/tv");
  });
  it("does not persist a default for a hidden field", () => {
    const d: PluginAdminForm = {
      fields: [
        {
          key: "is_4k",
          label: "4K",
          control: "SWITCH",
          required: false,
          secret: false,
          multiline: false,
        },
        {
          key: "season_folder",
          label: "Season folder",
          control: "SWITCH",
          required: false,
          secret: false,
          multiline: false,
          default_value: true,
          show_when: [{ field: "is_4k", equals: ["true"] }],
        },
      ],
    };
    const out = buildSchemaValues(d, { is_4k: false });
    expect(out.season_folder).toBeUndefined();
  });
});

describe("buildSchemaValues hidden fields", () => {
  it("drops a field hidden by show_when even if its draft value is set", () => {
    const d: PluginAdminForm = {
      fields: [
        {
          key: "is_4k",
          label: "4K",
          control: "SWITCH",
          required: false,
          secret: false,
          multiline: false,
        },
        {
          key: "is_default_4k",
          label: "Default 4K",
          control: "SWITCH",
          required: false,
          secret: false,
          multiline: false,
          show_when: [{ field: "is_4k", equals: ["true"] }],
        },
      ],
    };
    const out = buildSchemaValues(d, { is_4k: false, is_default_4k: true });
    expect(out.is_default_4k).toBeUndefined(); // hidden -> not persisted
    expect(out.is_4k).toBe(false);
  });
  it("keeps a field when its show_when is satisfied", () => {
    const d: PluginAdminForm = {
      fields: [
        {
          key: "is_4k",
          label: "4K",
          control: "SWITCH",
          required: false,
          secret: false,
          multiline: false,
        },
        {
          key: "is_default_4k",
          label: "Default 4K",
          control: "SWITCH",
          required: false,
          secret: false,
          multiline: false,
          show_when: [{ field: "is_4k", equals: ["true"] }],
        },
      ],
    };
    const out = buildSchemaValues(d, { is_4k: true, is_default_4k: true });
    expect(out.is_default_4k).toBe(true);
  });
});

const boolField: PluginAdminFormField = {
  key: "is_4k",
  label: "4K",
  control: "SWITCH",
  required: false,
  secret: false,
  multiline: false,
};

const numArrayField: PluginAdminFormField = {
  key: "weights",
  label: "Weights",
  control: "MULTI_SELECT",
  required: false,
  secret: false,
  multiline: false,
  dynamic_options: true,
};

describe("coerceFieldValue boolean coercion (CodeRabbit #4)", () => {
  it('does not turn the string "false" into true', () => {
    expect(coerceFieldValue(boolField, "false", "boolean")).toBe(false);
    expect(coerceFieldValue(boolField, "true", "boolean")).toBe(true);
    expect(coerceFieldValue(boolField, false, "boolean")).toBe(false);
    expect(coerceFieldValue(boolField, true, "boolean")).toBe(true);
  });
  it('does not turn the string "false" into true via the legacy SWITCH path', () => {
    expect(coerceFieldValue(boolField, "false")).toBe(false);
    expect(coerceFieldValue(boolField, "true")).toBe(true);
  });
});

describe("coerceFieldValue array:num coercion (CodeRabbit #5)", () => {
  it("coerces decimal numeric strings, not just integers", () => {
    expect(coerceFieldValue(numArrayField, ["1.5", "2"], "array:num")).toEqual([1.5, 2]);
  });
  it("leaves array:int as integer-only and non-numeric strings untouched", () => {
    expect(coerceFieldValue(numArrayField, ["1.5", "2"], "array:int")).toEqual(["1.5", 2]);
    expect(coerceFieldValue(numArrayField, ["abc"], "array:num")).toEqual(["abc"]);
  });
});
