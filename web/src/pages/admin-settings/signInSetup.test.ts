import { describe, expect, it } from "vitest";

import type { PluginInstallation } from "@/api/types";

import { missingSignInSetup, signInSetupLayout } from "./signInSetup";

function installation(overrides: Partial<PluginInstallation>): PluginInstallation {
  return {
    id: 1,
    plugin_id: "silo.auth.ldap",
    capabilities: [
      {
        type: "auth_provider.v1",
        id: "ldap",
        display_name: "LDAP",
        metadata: { connection_test: true },
      },
    ],
    global_config_schema: [],
    global_configs: [],
    ...overrides,
  } as unknown as PluginInstallation;
}

const field = (key: string, extra: Record<string, unknown> = {}) => ({
  key,
  label: key.toUpperCase(),
  control: "TEXT",
  required: false,
  secret: false,
  multiline: false,
  ...extra,
});

describe("signInSetupLayout", () => {
  it("makes sections steps, collapsible sections Advanced, and loose fields a step of their own", () => {
    const layout = signInSetupLayout(
      installation({
        global_config_schema: [
          {
            key: "directory",
            title: "Directory connection",
            description: "Where the directory is.",
            json_schema: "{}",
            required: false,
            admin_form: {
              fields: [field("urls"), field("bind_dn"), field("ca_pem")],
              sections: [
                {
                  key: "connect",
                  title: "Connect to the directory",
                  collapsible: false,
                  collapsed_default: false,
                  field_keys: ["urls", "bind_dn"],
                },
                {
                  key: "tls",
                  title: "TLS",
                  collapsible: true,
                  collapsed_default: true,
                  field_keys: ["ca_pem"],
                },
              ],
            },
          },
          {
            key: "groups",
            title: "Who can sign in",
            json_schema: "{}",
            required: false,
            admin_form: { fields: [field("allowed_groups")] },
          },
          {
            key: "display_name",
            title: "Button label",
            json_schema: "{}",
            required: false,
            admin_form: { fields: [field("value")] },
          },
        ] as never,
      }),
    );
    expect(layout.steps.map((group) => [group.id, group.title, group.description])).toEqual([
      ["directory.connect", "Connect to the directory", "Where the directory is."],
      ["groups", "Who can sign in", undefined],
    ]);
    expect(layout.advanced.map((group) => group.id)).toEqual(["directory.tls"]);
    expect(layout.loginName?.schemaKey).toBe("display_name");
  });

  it("moves the connection test's look-up field out of the steps", () => {
    const layout = signInSetupLayout(
      installation({
        capabilities: [
          {
            type: "auth_provider.v1",
            id: "ldap",
            display_name: "LDAP",
            metadata: { connection_test_username_field: "users.test_username" },
          },
        ] as never,
        global_config_schema: [
          {
            key: "users",
            title: "Find people",
            json_schema: "{}",
            required: false,
            admin_form: { fields: [field("base_dn"), field("test_username")] },
          },
        ] as never,
      }),
    );
    expect(layout.testUsername?.field.key).toBe("test_username");
    expect(layout.steps[0]!.fields.map((entry) => entry.key)).toEqual(["base_dn"]);
  });
});

describe("missingSignInSetup", () => {
  it("names required fields with nothing saved, secrets by their saved flag", () => {
    const base = {
      global_config_schema: [
        {
          key: "connection",
          title: "Connection",
          json_schema: "{}",
          required: true,
          admin_form: {
            fields: [
              field("issuer_url", { label: "Issuer URL", required: true }),
              field("client_secret", {
                label: "Client secret",
                control: "PASSWORD",
                secret: true,
                required: true,
              }),
              field("scopes"),
            ],
          },
        },
        {
          key: "extra",
          title: "Extra",
          json_schema: "{}",
          required: true,
          admin_form: { fields: [field("note")] },
        },
      ] as never,
    };
    expect(missingSignInSetup(signInSetupLayout(installation(base)))).toEqual([
      "Issuer URL",
      "Client secret",
      "Extra",
    ]);
    expect(
      missingSignInSetup(
        signInSetupLayout(
          installation({
            ...base,
            global_configs: [
              {
                key: "connection",
                value: { issuer_url: "https://id.example.test" },
                configured_secrets: ["client_secret"],
              },
              { key: "extra", value: {} },
            ],
          }),
        ),
      ),
    ).toEqual([]);
  });

  it("counts an entry the page can't render as missing only until it is saved", () => {
    const base = {
      global_config_schema: [
        { key: "custom", title: "Custom", json_schema: '{"type":"array"}', required: true },
      ] as never,
    };
    expect(missingSignInSetup(signInSetupLayout(installation(base)))).toEqual(["Custom"]);
    expect(
      missingSignInSetup(
        signInSetupLayout(
          installation({ ...base, global_configs: [{ key: "custom", value: {} }] }),
        ),
      ),
    ).toEqual([]);
  });
});
