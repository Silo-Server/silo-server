import { describe, expect, it } from "vitest";

import type { PluginConfigSchema } from "@/api/types";

import { adminFormForConfigSchema } from "./configSchemaAdminForm";

function schema(overrides: Partial<PluginConfigSchema> = {}): PluginConfigSchema {
  return {
    key: "connection",
    title: "Connection",
    json_schema: JSON.stringify({
      type: "object",
      properties: {},
      additionalProperties: false,
    }),
    required: true,
    ...overrides,
  };
}

describe("adminFormForConfigSchema", () => {
  it("preserves primitive JSON Schema defaults on inferred fields", () => {
    const form = adminFormForConfigSchema(
      schema({
        json_schema: JSON.stringify({
          type: "object",
          properties: {
            base_url: { type: "string", default: "https://floppy.example.com" },
            port: { type: "integer", default: 8080 },
            verify_tls: { type: "boolean", default: true },
          },
        }),
      }),
    );

    expect(form?.fields.map(({ key, default_value }) => ({ key, default_value }))).toEqual([
      { key: "base_url", default_value: "https://floppy.example.com" },
      { key: "port", default_value: 8080 },
      { key: "verify_tls", default_value: true },
    ]);
  });

  it("adds scalar schema properties omitted by a partial admin form", () => {
    const form = adminFormForConfigSchema(
      schema({
        json_schema: JSON.stringify({
          type: "object",
          properties: {
            base_url: { type: "string" },
            username: { type: "string" },
          },
          required: ["base_url", "username"],
        }),
        admin_form: {
          fields: [
            {
              key: "base_url",
              label: "Custom URL",
              control: "TEXT",
              required: true,
              secret: false,
              multiline: false,
            },
          ],
        },
      }),
    );

    expect(form?.fields.map(({ key, label, required }) => ({ key, label, required }))).toEqual([
      { key: "base_url", label: "Custom URL", required: true },
      { key: "username", label: "Username", required: true },
    ]);
  });

  it("preserves an explicit form when its schema cannot be inferred", () => {
    const explicit = {
      fields: [
        {
          key: "api_key",
          label: "API Key",
          control: "PASSWORD" as const,
          required: true,
          secret: true,
          multiline: false,
        },
      ],
    };
    expect(adminFormForConfigSchema(schema({ json_schema: "", admin_form: explicit }))).toBe(
      explicit,
    );
  });

  it("applies JSON Schema sensitivity to matching explicit fields", () => {
    const form = adminFormForConfigSchema(
      schema({
        json_schema: JSON.stringify({
          type: "object",
          properties: {
            api_key: { type: "string", writeOnly: true },
            password: { type: "string", format: "password" },
            advanced: { type: "object", writeOnly: true },
          },
        }),
        admin_form: {
          fields: [
            {
              key: "api_key",
              label: "API Key",
              control: "TEXT",
              required: false,
              secret: false,
              multiline: false,
            },
            {
              key: "password",
              label: "Password",
              control: "TEXT",
              required: false,
              secret: false,
              multiline: false,
            },
            {
              key: "advanced",
              label: "Advanced",
              control: "TEXTAREA",
              required: false,
              secret: false,
              multiline: true,
            },
          ],
        },
      }),
    );

    expect(form?.fields.map(({ key, secret }) => ({ key, secret }))).toEqual([
      { key: "api_key", secret: true },
      { key: "password", secret: true },
      { key: "advanced", secret: true },
    ]);
  });
});
