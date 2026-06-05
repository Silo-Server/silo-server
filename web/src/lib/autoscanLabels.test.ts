import { describe, expect, it } from "vitest";

import type { AutoscanSource } from "@/api/types";
import {
  composeSourceLabel,
  resolveEventSourceName,
  type SourceLabelLookups,
} from "./autoscanLabels";

describe("composeSourceLabel", () => {
  const base = { capabilityId: "arr", installationId: 4 };

  it("uses the operator label first, demoting connection to detail", () => {
    expect(
      composeSourceLabel({ ...base, operatorLabel: "4K Movies", connectionName: "Radarr4k" }),
    ).toEqual({ name: "4K Movies", detail: "Radarr4k · plugin #4" });
  });

  it("uses the connection name when no operator label", () => {
    expect(
      composeSourceLabel({ ...base, connectionName: "Radarr4k", displayName: "Arr Watcher" }),
    ).toEqual({ name: "Radarr4k", detail: "Arr Watcher · plugin #4" });
  });

  it("uses the manifest display name when no connection", () => {
    expect(
      composeSourceLabel({
        capabilityId: "cephfs",
        installationId: 5,
        displayName: "CephFS Watcher",
      }),
    ).toEqual({ name: "CephFS Watcher", detail: "plugin #5" });
  });

  it("falls back to capability id when nothing else is set", () => {
    expect(composeSourceLabel(base)).toEqual({ name: "arr", detail: "plugin #4" });
  });

  it("ignores whitespace-only rungs", () => {
    expect(composeSourceLabel({ ...base, operatorLabel: "   ", connectionName: "  " })).toEqual({
      name: "arr",
      detail: "plugin #4",
    });
  });
});

describe("resolveEventSourceName", () => {
  const source: AutoscanSource = {
    id: "src-1",
    installation_id: 4,
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
    displayNames: new Map([["4:arr", "Arr Watcher"]]),
  };

  it("resolves the connection name via the source reference", () => {
    expect(
      resolveEventSourceName(
        { source_id: "src-1", capability_id: "arr", installation_id: 4 },
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
        { source_id: "src-1", capability_id: "arr", installation_id: 4 },
        withLabel,
      ),
    ).toBe("4K Movies");
  });

  it("falls back to display name when the source was deleted (null source_id)", () => {
    expect(
      resolveEventSourceName(
        { source_id: null, capability_id: "arr", installation_id: 4 },
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
        { source_id: "src-1", capability_id: "arr", installation_id: 4 },
        orphaned,
      ),
    ).toBe("Arr Watcher");
  });
});
