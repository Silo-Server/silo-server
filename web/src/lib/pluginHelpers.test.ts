import { describe, expect, it } from "vitest";

import type { PluginInstallation } from "@/api/types";

import { capabilityKind, capabilityListLabel, catalogJobs } from "./pluginCapabilities";
import { parsePluginMarkdown } from "./pluginMarkdown";
import {
  licenseLabel,
  pluginDisplayName,
  pluginPagePath,
  pluginResourceLinks,
  safeExternalURL,
  sourceLabel,
  tierNotice,
} from "./pluginPresentation";
import {
  matchesInstalledFilter,
  parseInstalledFilter,
  pluginStatus,
  sortInstalledPlugins,
} from "./pluginStatus";
import { pluginStatusIndicator } from "./pluginStatusIndicator";
import { describeTrigger, pluginTaskPath, pluginTaskTrigger } from "./taskTrigger";

function installation(overrides: Partial<PluginInstallation> = {}): PluginInstallation {
  return {
    id: 1,
    repository_id: 1,
    plugin_id: "silo.test",
    version: "1.0.0",
    install_path: "/plugins/test",
    enabled: true,
    runtime: { resident: false, state: "stopped", restart_count: 0 },
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
  };
}

const PRESENTATION_STUB = {
  display_name: "",
  summary: "",
  description_markdown: "",
  setup_markdown: "",
  homepage_url: "",
  source_url: "",
  support_url: "",
  changelog_url: "",
  publisher_name: "",
  publisher_url: "",
  license_spdx: "",
};

const REQUIRED_ACCOUNT = [{ key: "account", title: "Account", json_schema: "{}", required: true }];

describe("pluginPresentation", () => {
  it("encodes plugin IDs into page paths", () => {
    expect(pluginPagePath("silo.mdblist")).toBe("/admin/plugins/silo.mdblist");
    expect(pluginPagePath("a/b?c#d")).toBe("/admin/plugins/a%2Fb%3Fc%23d");
    expect(pluginPagePath("silo.example", { repositoryId: 7, version: "2.0+beta" })).toBe(
      "/admin/plugins/silo.example?repository=7&version=2.0%2Bbeta",
    );
  });

  it("labels every tier and gives community and external plugins a notice", () => {
    expect(sourceLabel("silo")).toBe("Silo maintained");
    expect(sourceLabel("approved_community")).toBe("Approved community");
    expect(sourceLabel("external")).toBe("External source");
    expect(tierNotice("silo")).toBeNull();
    expect(tierNotice("approved_community")).toMatch(/Reviewed by Silo maintainers/);
    expect(tierNotice("external")).toMatch(/Silo has not reviewed this plugin/);
  });

  it("only allows http and https links", () => {
    expect(safeExternalURL("https://example.com/x")).toBe("https://example.com/x");
    expect(safeExternalURL("javascript:alert(1)")).toBeUndefined();
    expect(safeExternalURL("data:text/html,x")).toBeUndefined();
    expect(safeExternalURL("not a url")).toBeUndefined();
    expect(
      pluginResourceLinks(undefined, "https://github.com/Silo-Server/x").map((l) => l.label),
    ).toEqual(["Source code"]);
    // A rejected presentation URL falls back to the repository URL.
    expect(
      pluginResourceLinks(
        {
          display_name: "",
          summary: "",
          description_markdown: "",
          setup_markdown: "",
          homepage_url: "",
          source_url: "javascript:alert(1)",
          support_url: "",
          changelog_url: "",
          publisher_name: "",
          publisher_url: "",
          license_spdx: "",
        },
        "https://github.com/Silo-Server/x",
      ),
    ).toEqual([{ label: "Source code", url: "https://github.com/Silo-Server/x" }]);
  });

  it("falls back to a readable name and license", () => {
    expect(pluginDisplayName("silo.requests.seerr")).toBe("Requests Seerr");
    expect(pluginDisplayName("silo")).toBe("silo");
    expect(pluginDisplayName("silo_")).toBe("silo_");
    expect(licenseLabel("NOASSERTION")).toBe("Not specified");
    expect(licenseLabel("")).toBe("Not specified");
    expect(licenseLabel("MIT")).toBe("MIT");
  });
});

describe("pluginCapabilities", () => {
  it("maps known capability types and falls back for unknown ones", () => {
    expect(capabilityKind("metadata_provider.v1")).toMatchObject({ label: "Metadata" });
    expect(capabilityKind("http_routes.v1").primary).toBe(false);
    expect(capabilityKind("weird_thing.v2")).toMatchObject({
      label: "Weird thing",
      primary: false,
    });
  });

  it("lists a plugin's jobs once each, in a fixed order, without plumbing", () => {
    expect(
      capabilityListLabel([
        { type: "image_resolver.v1", id: "a", display_name: "" },
        { type: "http_routes.v1", id: "b", display_name: "" },
        { type: "metadata_provider.v1", id: "c", display_name: "" },
        { type: "metadata_provider.v1", id: "d", display_name: "" },
      ]),
    ).toBe("Metadata, Artwork");
    expect(
      catalogJobs([
        [{ type: "marker_provider.v1", id: "m", display_name: "" }],
        [{ type: "metadata_provider.v1", id: "t", display_name: "" }],
      ]).map((kind) => kind.job),
    ).toEqual(["metadata", "markers"]);
  });
});

describe("pluginStatus", () => {
  it("ranks failures, restarts, and missing settings as needing attention", () => {
    expect(
      pluginStatus(
        installation({ runtime: { resident: true, state: "failed", restart_count: 1 } }),
      ),
    ).toMatchObject({ label: "Failed", attention: true, rank: 0 });
    expect(
      pluginStatus(
        installation({ runtime: { resident: true, state: "backoff", restart_count: 3 } }),
      ),
    ).toMatchObject({ label: "Restarting (3)", attention: true, rank: 1 });
    expect(pluginStatus(installation({ global_config_schema: REQUIRED_ACCOUNT }))).toMatchObject({
      label: "Needs setup",
      attention: true,
      rank: 2,
    });
    expect(pluginStatus(installation())).toMatchObject({ label: "Active", attention: false });
  });

  it("never asks for attention on a plugin that is off", () => {
    const off = installation({
      enabled: false,
      global_config_schema: REQUIRED_ACCOUNT,
      runtime: { resident: true, state: "failed", restart_count: 1 },
    });
    expect(pluginStatus(off)).toMatchObject({ label: "Off", attention: false });
    expect(matchesInstalledFilter(off, "off")).toBe(true);
    expect(matchesInstalledFilter(off, "attention")).toBe(false);
  });

  it("sorts by the same catalog fallback name the tiles show", () => {
    const older = installation({ id: 1, plugin_id: "silo.zzz", presentation: undefined });
    const named = installation({ id: 2, plugin_id: "silo.mmm" });
    expect(sortInstalledPlugins([named, older]).map((entry) => entry.id)).toEqual([2, 1]);
    expect(
      sortInstalledPlugins([named, older], (entry) =>
        entry.id === 1 ? { ...PRESENTATION_STUB, display_name: "Aardvark" } : entry.presentation,
      ).map((entry) => entry.id),
    ).toEqual([1, 2]);
  });

  it("sorts problems first and off plugins last", () => {
    const sorted = sortInstalledPlugins([
      installation({ id: 1, plugin_id: "silo.zulu", enabled: false }),
      installation({ id: 2, plugin_id: "silo.beta" }),
      installation({ id: 3, plugin_id: "silo.alpha" }),
      installation({ id: 4, plugin_id: "silo.keyed", global_config_schema: REQUIRED_ACCOUNT }),
    ]);
    expect(sorted.map((entry) => entry.id)).toEqual([4, 3, 2, 1]);
  });

  it("parses the filter param and matches updates", () => {
    expect(parseInstalledFilter("update")).toBe("update");
    expect(parseInstalledFilter("bogus")).toBe("all");
    expect(parseInstalledFilter(null)).toBe("all");
    expect(matchesInstalledFilter(installation({ available_version: "2.0.0" }), "update")).toBe(
      true,
    );
  });
});

describe("pluginStatusIndicator", () => {
  it("reads runtime.state only for resident plugins and calls disabled plugins Off", () => {
    const base = installation();
    expect(pluginStatusIndicator(base)).toMatchObject({ dotClass: "bg-success", label: "Active" });
    expect(
      pluginStatusIndicator({
        ...base,
        runtime: { resident: true, state: "running", restart_count: 0 },
      }),
    ).toMatchObject({ dotClass: "bg-success", label: "Running" });
    expect(
      pluginStatusIndicator({
        ...base,
        runtime: { resident: true, state: "backoff", restart_count: 3, last_error: "exited" },
      }),
    ).toMatchObject({ dotClass: "bg-warning", label: "Restarting (3)", title: "exited" });
    expect(
      pluginStatusIndicator({
        ...base,
        runtime: { resident: true, state: "failed", restart_count: 9, last_error: "boom" },
      }),
    ).toMatchObject({ dotClass: "bg-destructive", label: "Failed", title: "boom" });
    expect(
      pluginStatusIndicator({
        ...base,
        enabled: false,
        runtime: { resident: true, state: "failed", restart_count: 9 },
      }),
    ).toMatchObject({ dotClass: "bg-muted-foreground", label: "Off" });
  });
});

describe("taskTrigger", () => {
  it("keeps the task list's short wording and the detail page's long wording", () => {
    expect(describeTrigger({ type: "interval", interval_ms: 3 * 3_600_000 }, "short")).toBe(
      "Every 3h",
    );
    expect(describeTrigger({ type: "interval", interval_ms: 2 * 86_400_000 }, "short")).toBe(
      "Every 2d",
    );
    expect(describeTrigger({ type: "startup" }, "short")).toBe("On startup");
    expect(describeTrigger({ type: "weekly", day_of_week: 1, time_of_day: "04:00" }, "short")).toBe(
      "Mon at 04:00",
    );
    expect(describeTrigger({ type: "interval", interval_ms: 3 * 3_600_000 })).toBe(
      "Every 3 hour(s)",
    );
    expect(describeTrigger({ type: "startup" })).toBe("On server startup");
    expect(describeTrigger({ type: "weekly", day_of_week: 1, time_of_day: "04:00" })).toBe(
      "Monday at 04:00",
    );
    expect(describeTrigger({ type: "daily", time_of_day: "03:00" })).toBe("Daily at 03:00");
    expect(describeTrigger({ type: "interval", interval_ms: 5_400_000 })).toBe(
      "Every 90 minute(s)",
    );
    expect(describeTrigger({ type: "interval", interval_ms: 90_000 }, "short")).toBe("Every 90s");
  });

  it("defaults a missing or unreadable plugin trigger to startup, like the server", () => {
    expect(pluginTaskTrigger(undefined)).toEqual({ type: "startup" });
    expect(pluginTaskTrigger({ type: "" })).toEqual({ type: "startup" });
    // The server builds no live trigger for an unknown type, so don't claim startup.
    expect(pluginTaskTrigger({ type: "sometimes" })).toEqual({ type: "sometimes" });
    expect(pluginTaskTrigger({ type: "interval", interval_ms: "60000" })).toEqual({
      type: "startup",
    });
    expect(describeTrigger(pluginTaskTrigger({ type: "sometimes" }))).toBe("sometimes");
    expect(describeTrigger({ type: "weekly", day_of_week: 7, time_of_day: "02:00" })).toBe(
      "Day 7 at 02:00",
    );
    expect(pluginTaskTrigger({ type: "daily", time_of_day: "02:00" })).toEqual({
      type: "daily",
      time_of_day: "02:00",
    });
    expect(pluginTaskPath(7, "refresh")).toBe("/admin/tasks/plugin%3A7%3Arefresh");
  });
});

describe("parsePluginMarkdown", () => {
  it("reads paragraphs, lists, and inline marks", () => {
    expect(
      parsePluginMarkdown(
        "Add **MDBList** to a library.\nSee `dump_path`.\n\n- first\n- [docs](https://x.test)\n\n1. one\n2. two",
      ),
    ).toEqual([
      {
        kind: "paragraph",
        inlines: [
          { kind: "text", text: "Add " },
          { kind: "strong", text: "MDBList" },
          { kind: "text", text: " to a library. See " },
          { kind: "code", text: "dump_path" },
          { kind: "text", text: "." },
        ],
      },
      {
        kind: "list",
        ordered: false,
        items: [
          [{ kind: "text", text: "first" }],
          [{ kind: "link", text: "docs", href: "https://x.test" }],
        ],
      },
      {
        kind: "list",
        ordered: true,
        items: [[{ kind: "text", text: "one" }], [{ kind: "text", text: "two" }]],
      },
    ]);
  });

  it("keeps HTML and snake_case identifiers as literal text", () => {
    expect(parsePluginMarkdown("<b>x</b> and dump_path_value")).toEqual([
      { kind: "paragraph", inlines: [{ kind: "text", text: "<b>x</b> and dump_path_value" }] },
    ]);
    expect(parsePluginMarkdown("  ")).toEqual([]);
    expect(parsePluginMarkdown(undefined)).toEqual([]);
  });
});
