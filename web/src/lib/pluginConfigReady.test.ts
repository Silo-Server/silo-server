import { describe, expect, it } from "vitest";

import type { PluginInstallation } from "@/api/types";

import { installationConfigReady } from "./pluginConfigReady";

function installation(overrides: Partial<PluginInstallation>): PluginInstallation {
  return {
    id: 1,
    repository_id: 1,
    plugin_id: "silo.test",
    version: "1.0.0",
    install_path: "/plugins/test",
    enabled: true,
    source_kind: "silo",
    updates_paused: false,
    capabilities: [],
    global_config_schema: [],
    user_config_schema: [],
    routes: [],
    assets: [],
    global_configs: [],
    auth_bindings: [],
    task_bindings: [],
    update_policy: "auto",
    ...overrides,
  } as PluginInstallation;
}

const schema = (key: string, required: boolean) => ({
  key,
  title: key,
  json_schema: "{}",
  required,
});

describe("installationConfigReady", () => {
  it("is ready when the plugin declares no configuration", () => {
    expect(installationConfigReady(installation({}))).toBe(true);
  });

  it("is not ready when configuration is declared but nothing is saved", () => {
    expect(
      installationConfigReady(
        installation({ global_config_schema: [schema("account", true)], global_configs: [] }),
      ),
    ).toBe(false);
  });

  it("counts configured secrets as filled", () => {
    expect(
      installationConfigReady(
        installation({
          global_config_schema: [schema("account", true)],
          global_configs: [{ key: "account", value: {}, configured_secrets: ["api_key"] }],
        }),
      ),
    ).toBe(true);
  });

  it("counts a required boolean explicitly saved as false as configured", () => {
    // `false` is a value the admin deliberately chose; only a blank string is
    // "no value" (that exclusion keeps a keyless plugin from reading ready).
    expect(
      installationConfigReady(
        installation({
          global_config_schema: [schema("advanced", true)],
          global_configs: [{ key: "advanced", value: { advanced: false }, configured_secrets: [] }],
        }),
      ),
    ).toBe(true);
  });

  it("does not count a blank string as a value", () => {
    expect(
      installationConfigReady(
        installation({
          global_config_schema: [schema("account", true)],
          global_configs: [{ key: "account", value: { api_key: "   " }, configured_secrets: [] }],
        }),
      ),
    ).toBe(false);
  });
});
