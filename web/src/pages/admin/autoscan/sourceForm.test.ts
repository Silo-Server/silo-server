import { describe, expect, it } from "vitest";

import type { AutoscanScanSourceDescriptor } from "@/api/types";
import { pollSource, webhookSource } from "@/test/autoscanServer";

import {
  draftFromSource,
  editedSourceBody,
  incompleteMappings,
  parseIntervalInput,
  sourceConfigForEdit,
  sourceUpdateBody,
} from "./sourceForm";
import { newMapping } from "./webhookSetup";

const descriptor: AutoscanScanSourceDescriptor = {
  delivery_modes: ["poll"],
  connection: "optional",
};

describe("sourceUpdateBody", () => {
  it("resends every stored field so a PUT clears nothing", () => {
    const source = pollSource({ label: "TV", poll_interval_seconds: 900 });
    expect(sourceUpdateBody(source, { enabled: false })).toEqual({
      connection_id: "conn-sonarr",
      enabled: false,
      delivery_mode: "poll",
      poll_interval_seconds: 900,
      path_rewrites: [{ from: "/data/tv", to: "/mnt/media/tv" }],
      source_config: { lookback: "24h" },
      label: "TV",
    });
  });
});

describe("editedSourceBody", () => {
  it("builds the PUT from the draft and the live enabled state", () => {
    const source = pollSource();
    const draft = {
      ...draftFromSource(source, descriptor),
      label: "  Basement ",
      intervalStr: "1200",
      mappings: [newMapping("/mnt/media/tv", " /data/tv "), newMapping("", "")],
    };
    expect(
      editedSourceBody({ ...source, enabled: false }, draft, { lookback: " 48h ", " ": "x" }),
    ).toEqual({
      connection_id: "conn-sonarr",
      enabled: false,
      delivery_mode: "poll",
      poll_interval_seconds: 1200,
      path_rewrites: [{ from: "/data/tv", to: "/mnt/media/tv" }],
      source_config: { lookback: "48h" },
      label: "Basement",
    });
  });

  it("keeps the stored interval rather than clearing it on bad input", () => {
    const source = pollSource({ poll_interval_seconds: 900 });
    const draft = { ...draftFromSource(source, descriptor), intervalStr: "abc" };
    expect(editedSourceBody(source, draft, {}).poll_interval_seconds).toBe(900);
  });

  it("treats the picker's none sentinel as no connection", () => {
    const source = pollSource();
    const draft = { ...draftFromSource(source, descriptor), connectionId: "__none__" };
    expect(editedSourceBody(source, draft, {}).connection_id).toBeNull();
  });

  it("round-trips a webhook source unchanged", () => {
    const source = webhookSource();
    const draft = draftFromSource(source, descriptor);
    expect(editedSourceBody(source, draft, { webhook_provider: "sonarr" })).toEqual(
      sourceUpdateBody(source),
    );
  });
});

describe("draft helpers", () => {
  it("parses the interval field", () => {
    expect(parseIntervalInput("")).toEqual({ valid: true, seconds: null });
    expect(parseIntervalInput(" 60 ")).toEqual({ valid: true, seconds: 60 });
    expect(parseIntervalInput("0")).toEqual({ valid: false });
    expect(parseIntervalInput("1.5")).toEqual({ valid: false });
    // The server stores a 32-bit signed integer and refuses anything larger.
    expect(parseIntervalInput("2147483647")).toEqual({ valid: true, seconds: 2147483647 });
    expect(parseIntervalInput("2147483648")).toEqual({ valid: false });
    expect(parseIntervalInput("1e12")).toEqual({ valid: false });
  });

  it("flags rows with only one side filled in", () => {
    const half = newMapping("/mnt/media", "");
    expect(incompleteMappings([newMapping("", ""), newMapping("/a", "/b"), half])).toEqual([half]);
  });

  it("folds legacy CephFS keys into their replacement only for that plugin", () => {
    const ceph = pollSource({
      plugin_id: "silo.autoscan.cephfs",
      capability_id: "cephfs",
      source_config: { tv_nested_paths: "/b\n/a", tv_flat_paths: "/a" },
    });
    expect(sourceConfigForEdit(ceph)).toEqual({ tv_flat_paths: "/a\n/b" });
    const other = pollSource({ source_config: { tv_nested_paths: "/b" } });
    expect(sourceConfigForEdit(other)).toEqual({ tv_nested_paths: "/b" });
  });
});
