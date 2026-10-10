import { describe, expect, it } from "vitest";
import type { AutoscanSettings, AutoscanSource } from "@/api/types";
import { showRunNow } from "./runNow";

const settings: AutoscanSettings = {
  enabled: true,
  default_poll_interval_seconds: 600,
  debounce_seconds: 60,
};

const source = (overrides: Partial<AutoscanSource> = {}): AutoscanSource =>
  ({
    id: "poll",
    plugin_id: "plugin",
    capability_id: "scan_source",
    connection_id: null,
    enabled: true,
    delivery_mode: "poll",
    poll_interval_seconds: null,
    path_rewrites: [],
    source_config: {},
    label: "",
    last_run_at: null,
    last_error: null,
    ...overrides,
  }) as AutoscanSource;

describe("showRunNow", () => {
  it("shows Run now when Autoscan is on and an enabled source polls", () => {
    expect(showRunNow([source()], settings)).toBe(true);
    // A source polled moments ago still counts: Run now ignores intervals.
    expect(showRunNow([source({ last_run_at: new Date().toISOString() })], settings)).toBe(true);
    expect(showRunNow([source({ delivery_mode: "webhook" }), source({ id: "b" })], settings)).toBe(
      true,
    );
  });

  it("hides Run now when no enabled source is polled", () => {
    expect(showRunNow([], settings)).toBe(false);
    expect(showRunNow([source({ delivery_mode: "webhook" })], settings)).toBe(false);
    expect(showRunNow([source({ enabled: false })], settings)).toBe(false);
  });

  it("hides Run now while Autoscan is off", () => {
    expect(showRunNow([source()], { ...settings, enabled: false })).toBe(false);
    expect(showRunNow(undefined, { ...settings, enabled: false })).toBe(false);
  });

  it("shows Run now while settings or sources are not loaded", () => {
    expect(showRunNow([source()], undefined)).toBe(true);
    expect(showRunNow(undefined, settings)).toBe(true);
    expect(showRunNow(undefined, undefined)).toBe(true);
  });
});
