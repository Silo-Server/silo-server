import { describe, expect, it } from "vitest";

import type { AutoscanSource } from "@/api/types";
import {
  composeSourceLabel,
  resolveEventSourceName,
  type SourceLabelLookups,
} from "./autoscanLabels";

describe("composeSourceLabel", () => {
  const base = { capabilityId: "arr", pluginId: "silo.autoscan.arr" };

  it("uses the operator label first, demoting connection to detail", () => {
    expect(
      composeSourceLabel({ ...base, operatorLabel: "4K Movies", connectionName: "Radarr4k" }),
    ).toEqual({ name: "4K Movies", detail: "Radarr4k · silo.autoscan.arr" });
  });

  it("uses the connection name when no operator label", () => {
    expect(
      composeSourceLabel({ ...base, connectionName: "Radarr4k", displayName: "Arr Watcher" }),
    ).toEqual({ name: "Radarr4k", detail: "Arr Watcher · silo.autoscan.arr" });
  });

  it("uses the manifest display name when no connection", () => {
    expect(
      composeSourceLabel({
        capabilityId: "cephfs",
        pluginId: "silo.autoscan.cephfs",
        displayName: "CephFS Watcher",
      }),
    ).toEqual({ name: "CephFS Watcher", detail: "silo.autoscan.cephfs" });
  });

  it("falls back to capability id when nothing else is set", () => {
    expect(composeSourceLabel(base)).toEqual({ name: "arr", detail: "silo.autoscan.arr" });
  });

  it("ignores whitespace-only rungs", () => {
    expect(composeSourceLabel({ ...base, operatorLabel: "   ", connectionName: "  " })).toEqual({
      name: "arr",
      detail: "silo.autoscan.arr",
    });
  });
});

describe("resolveEventSourceName", () => {
  const source: AutoscanSource = {
    id: "src-1",
    plugin_id: "silo.autoscan.arr",
    capability_id: "arr",
    connection_id: "conn-1",
    enabled: true,
    poll_interval_seconds: null,
    last_run_at: null,
    last_error: null,
    path_rewrites: [],
    source_config: {},
    label: "",
  };
  const lookups: SourceLabelLookups = {
    sourceByID: new Map([["src-1", source]]),
    connectionByID: new Map([["conn-1", "Radarr4k"]]),
    displayNames: new Map([["silo.autoscan.arr:arr", "Arr Watcher"]]),
  };

  it("resolves the connection name via the source reference", () => {
    expect(
      resolveEventSourceName(
        { source_id: "src-1", capability_id: "arr", plugin_id: "silo.autoscan.arr" },
        lookups,
      ),
    ).toBe("Radarr4k");
  });

  it("prefers the operator label on the source", () => {
    const withLabel: SourceLabelLookups = {
      ...lookups,
      sourceByID: new Map([["src-1", { ...source, label: "4K Movies" }]]),
    };
    expect(
      resolveEventSourceName(
        { source_id: "src-1", capability_id: "arr", plugin_id: "silo.autoscan.arr" },
        withLabel,
      ),
    ).toBe("4K Movies");
  });

  it("falls back to display name when the source was deleted (null source_id)", () => {
    expect(
      resolveEventSourceName(
        { source_id: null, capability_id: "arr", plugin_id: "silo.autoscan.arr" },
        lookups,
      ),
    ).toBe("Arr Watcher");
  });

  it("returns empty string when the reference has no capability", () => {
    expect(resolveEventSourceName({ source_id: null }, lookups)).toBe("");
  });

  it("falls back to display name when the bound connection is missing (deleted)", () => {
    const orphaned: SourceLabelLookups = {
      ...lookups,
      sourceByID: new Map([["src-1", { ...source, connection_id: "conn-gone" }]]),
    };
    expect(
      resolveEventSourceName(
        { source_id: "src-1", capability_id: "arr", plugin_id: "silo.autoscan.arr" },
        orphaned,
      ),
    ).toBe("Arr Watcher");
  });
});
