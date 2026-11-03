// @vitest-environment node

import type { PluginAdminForm, PluginAdminFormField } from "@/api/types";
import { describe, expect, it } from "vitest";
import {
  buildSchemaValues,
  coerceFieldValue,
  parseFieldTypes,
  validateSchemaValues,
} from "./schemaFormUtils";
import { field } from "./adminFormField.test-support";

const descriptor: PluginAdminForm = {
  fields: [
    field("service_kind", "Service", "SELECT", {
      required: true,
      options: [
        { value: "radarr", label: "Radarr" },
        { value: "sonarr", label: "Sonarr" },
      ],
    }),
    field("quality_profile_id", "QP", "SELECT", { required: true, dynamic_options: true }),
    field("tags", "Tags", "MULTI_SELECT", { dynamic_options: true }),
    field("season_folder", "Season folder", "SWITCH", {
      show_when: [{ field: "service_kind", equals: ["sonarr"] }],
    }),
  ],
};

describe("validateSchemaValues", () => {
  it("ignores required fields hidden by show_when", () => {
    const d: PluginAdminForm = {
      fields: [
        field("x", "X", "TEXT", { required: true, show_when: [{ field: "k", equals: ["yes"] }] }),
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
      fields: [field("name", "Name", "TEXT", { validation: { pattern: "[" } })],
    };
    expect(() => validateSchemaValues(d, { name: "anything" })).not.toThrow();
    expect(validateSchemaValues(d, { name: "anything" })).toEqual({});
  });
  it("still flags a valid pattern mismatch", () => {
    const d: PluginAdminForm = {
      fields: [field("name", "Name", "TEXT", { validation: { pattern: "^[a-z]+$" } })],
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
        flags: { type: "array", items: { type: "boolean" } },
        enabled: { type: "boolean" },
      },
    });
    expect(parseFieldTypes(json)).toEqual({
      quality_profile_id: "integer",
      root_folder: "string",
      tags: "array:int",
      labels: "array",
      flags: "array:bool",
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
        field("quality_profile_id", "QP", "SELECT", { dynamic_options: true }),
        field("root_folder", "Root", "SELECT", { dynamic_options: true }),
        field("tags", "Tags", "MULTI_SELECT", { dynamic_options: true }),
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
      fields: [field("season_folder", "Season folder", "SWITCH", { default_value: true })],
    };
    const out = buildSchemaValues(d, {});
    expect(out.season_folder).toBe(true);
  });
  it("required field satisfied by its default value", () => {
    const d: PluginAdminForm = {
      fields: [
        field("season_folder", "Season folder", "SWITCH", { required: true, default_value: true }),
      ],
    };
    expect(validateSchemaValues(d, {})).toEqual({});
  });
  it("uses a controller default when validating a conditional required field", () => {
    const d: PluginAdminForm = {
      fields: [
        field("anime_enabled", "Anime", "SWITCH", { default_value: true }),
        field("anime_root_folder", "Anime root folder", "TEXT", {
          required: true,
          show_when: [{ field: "anime_enabled", equals: ["true"] }],
        }),
      ],
    };
    expect(validateSchemaValues(d, {}).anime_root_folder).toMatch(/required/i);
  });
  it("uses a controller default before dropping hidden fields", () => {
    const d: PluginAdminForm = {
      fields: [
        field("season_folder", "Season folder", "SWITCH", { default_value: true }),
        field("series_root_folder", "Series root folder", "TEXT", {
          default_value: "/tv",
          show_when: [{ field: "season_folder", equals: ["true"] }],
        }),
      ],
    };
    expect(buildSchemaValues(d, {}).series_root_folder).toBe("/tv");
  });
});

describe("buildSchemaValues hidden fields", () => {
  it("drops a field hidden by show_when even if its draft value is set", () => {
    const d: PluginAdminForm = {
      fields: [
        field("is_4k", "4K", "SWITCH"),
        field("is_default_4k", "Default 4K", "SWITCH", {
          show_when: [{ field: "is_4k", equals: ["true"] }],
        }),
      ],
    };
    const out = buildSchemaValues(d, { is_4k: false, is_default_4k: true });
    expect(out.is_default_4k).toBeUndefined(); // hidden -> not persisted
    expect(out.is_4k).toBe(false);
  });
});

const boolField: PluginAdminFormField = field("is_4k", "4K", "SWITCH");

const numArrayField: PluginAdminFormField = field("weights", "Weights", "MULTI_SELECT", {
  dynamic_options: true,
});

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

describe("coerceFieldValue array:bool coercion", () => {
  it("coerces boolean multi-select values using their declared item type", () => {
    expect(coerceFieldValue(numArrayField, ["true", "false", true], "array:bool")).toEqual([
      true,
      false,
      true,
    ]);
  });
});

describe("section visibility", () => {
  const sectionDescriptor: PluginAdminForm = {
    fields: [
      field("advanced_enabled", "Advanced", "SWITCH", { default_value: false }),
      field("endpoint", "Endpoint", "TEXT", { required: true }),
    ],
    sections: [
      {
        key: "advanced",
        title: "Advanced",
        collapsible: false,
        collapsed_default: false,
        field_keys: ["endpoint"],
        show_when: [{ field: "advanced_enabled", equals: ["true"] }],
      },
    ],
  };

  it("does not validate required fields in a hidden section", () => {
    expect(validateSchemaValues(sectionDescriptor, { endpoint: "stale" })).toEqual({});
    expect(validateSchemaValues(sectionDescriptor, { advanced_enabled: true }).endpoint).toMatch(
      /required/i,
    );
  });

  it("does not persist stale values from a hidden section", () => {
    expect(buildSchemaValues(sectionDescriptor, { endpoint: "stale" })).toEqual({
      advanced_enabled: false,
    });
    expect(
      buildSchemaValues(sectionDescriptor, { advanced_enabled: true, endpoint: "active" }),
    ).toEqual({ advanced_enabled: true, endpoint: "active" });
  });
});

describe("buildSchemaValues explicit clears", () => {
  const clearDescriptor: PluginAdminForm = {
    fields: [
      field("allowed_groups", "Allowed", "TEXT"),
      field("ca_pem", "CA", "TEXTAREA", { multiline: true }),
      field("max_age", "Max age", "NUMBER"),
      field("client_secret", "Secret", "PASSWORD", { secret: true }),
      field("token", "Token", "TEXT", { secret: true }),
    ],
  };
  const emptied = { allowed_groups: "", ca_pem: "  ", max_age: "", client_secret: "", token: "" };

  it("leaves emptied fields out by default", () => {
    expect(buildSchemaValues(clearDescriptor, emptied)).toEqual({});
  });

  it("sends emptied non-secret fields as explicit clears and never clears a secret", () => {
    expect(
      buildSchemaValues(clearDescriptor, emptied, undefined, { explicitClears: true }),
    ).toEqual({
      allowed_groups: "",
      ca_pem: "",
      max_age: null,
    });
  });

  it("follows the declared type when one is known", () => {
    const fieldTypes = parseFieldTypes(
      JSON.stringify({
        type: "object",
        properties: {
          allowed_groups: { type: "string" },
          max_age: { type: "integer" },
          ca_pem: { type: "string" },
        },
      }),
    );
    expect(
      buildSchemaValues(clearDescriptor, emptied, fieldTypes, { explicitClears: true }),
    ).toEqual({
      allowed_groups: "",
      ca_pem: "",
      max_age: null,
    });
  });
});
