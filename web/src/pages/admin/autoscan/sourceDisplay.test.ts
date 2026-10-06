import { describe, expect, it } from "vitest";

import { pollSource, webhookSource } from "@/test/autoscanServer";

import type { SourceLabelLookups } from "@/lib/autoscanLabels";

import {
  describeSource,
  effectivePollIntervalSeconds,
  eventSourceName,
  formatInterval,
  sourceHealth,
} from "./sourceDisplay";

const pluginDisplayNames = new Map([
  ["silo.autoscan.arr:arr", "Sonarr / Radarr"],
  ["silo.autoscan.arr-webhook:arr-webhook", "Sonarr/Radarr Webhook"],
]);
const connectionNames = new Map([["conn-sonarr", "Sonarr 4K"]]);

function describe_(source: Parameters<typeof describeSource>[0]["source"], seconds = 600) {
  return describeSource({
    source,
    pluginDisplayNames,
    connectionNames,
    defaultPollSeconds: seconds,
  });
}

describe("describeSource", () => {
  it("uses the provider for an unlabelled arr webhook", () => {
    expect(describe_(webhookSource())).toEqual({
      title: "Sonarr",
      subtitle: "Sonarr/Radarr Webhook",
      typeName: "Sonarr/Radarr Webhook",
    });
  });

  it("keeps the type and provider in the subtitle when a label is set", () => {
    expect(describe_(webhookSource({ label: "  4K shows " }))).toMatchObject({
      title: "4K shows",
      subtitle: "Sonarr/Radarr Webhook · Sonarr",
    });
  });

  it("says an unset provider is detected per delivery", () => {
    expect(describe_(webhookSource({ source_config: { webhook_provider: "auto" } }))).toMatchObject(
      { title: "Sonarr/Radarr Webhook", subtitle: "Webhook · auto-detects Sonarr or Radarr" },
    );
  });

  it("names the connection and effective interval for a polling source", () => {
    expect(describe_(pollSource({ label: "Basement" }))).toMatchObject({
      title: "Basement",
      subtitle: "Sonarr / Radarr · via Sonarr 4K · polls every 10 min",
    });
    expect(
      describe_(pollSource({ connection_id: null, poll_interval_seconds: 7200 })),
    ).toMatchObject({ title: "Sonarr / Radarr", subtitle: "Polls every 2 h" });
  });

  it("falls back to the capability when the plugin is not installed", () => {
    expect(
      describe_(pollSource({ plugin_id: "gone", capability_id: "gone-cap", connection_id: null }))
        .title,
    ).toBe("gone-cap");
  });
});

describe("eventSourceName", () => {
  const lookups = (
    sources: Parameters<typeof describeSource>[0]["source"][],
  ): SourceLabelLookups => ({
    sourceByID: new Map(sources.map((source) => [source.id, source])),
    connectionByID: connectionNames,
    displayNames: pluginDisplayNames,
  });
  const ref = (source: { id: string; plugin_id: string; capability_id: string }) => ({
    source_id: source.id,
    plugin_id: source.plugin_id,
    capability_id: source.capability_id,
  });

  it("names a source in Activity as the list titles it", () => {
    const sources = [
      webhookSource({ id: "hook-sonarr" }),
      webhookSource({ id: "hook-radarr", source_config: { webhook_provider: "radarr" } }),
      webhookSource({ id: "hook-auto", source_config: {} }),
      webhookSource({ id: "hook-label", label: " 4K shows " }),
      pollSource({ id: "poll-label", label: "Basement" }),
      pollSource({ id: "poll-unbound", connection_id: null }),
    ];
    const names = lookups(sources);
    for (const source of sources) {
      expect(eventSourceName(ref(source), names)).toBe(describe_(source).title);
    }
  });

  it("adds the server to an unlabelled polling source, as the list's second line does", () => {
    const source = pollSource();
    expect(describe_(source).subtitle).toBe("Via Sonarr 4K · polls every 10 min");
    expect(eventSourceName(ref(source), lookups([source]))).toBe("Sonarr / Radarr · via Sonarr 4K");
  });

  it("falls back to the plugin name for a deleted source or connection", () => {
    const source = pollSource({ connection_id: "conn-gone" });
    expect(eventSourceName(ref(source), lookups([source]))).toBe("Sonarr / Radarr");
    expect(eventSourceName({ ...ref(source), source_id: null }, lookups([]))).toBe(
      "Sonarr / Radarr",
    );
    expect(
      eventSourceName(
        { source_id: null, plugin_id: "gone", capability_id: "gone-cap" },
        lookups([]),
      ),
    ).toBe("gone-cap");
  });

  it("returns an empty name for a reference without a plugin", () => {
    expect(eventSourceName({ source_id: null }, lookups([]))).toBe("");
  });
});

describe("poll interval", () => {
  it("treats a per-source interval as a floor over the default", () => {
    expect(effectivePollIntervalSeconds(null, 600)).toBe(600);
    expect(effectivePollIntervalSeconds(60, 600)).toBe(600);
    expect(effectivePollIntervalSeconds(1800, 600)).toBe(1800);
    expect(effectivePollIntervalSeconds(1800, null)).toBe(1800);
    expect(effectivePollIntervalSeconds(null, null)).toBeNull();
  });

  it("formats intervals compactly", () => {
    expect(formatInterval(45)).toBe("45 s");
    expect(formatInterval(600)).toBe("10 min");
    expect(formatInterval(3600)).toBe("1 h");
    expect(formatInterval(5400)).toBe("1 h 30 min");
    expect(formatInterval(86400)).toBe("1 day");
    expect(formatInterval(172800)).toBe("2 days");
  });
});

describe("sourceHealth", () => {
  it("reports a disabled source as off before anything else", () => {
    expect(sourceHealth(pollSource({ enabled: false, last_error: "boom" }))).toEqual({
      kind: "off",
    });
  });

  it("prefers the newer of a webhook's delivery and error", () => {
    expect(
      sourceHealth(
        webhookSource({
          webhook_last_received_at: "2026-01-01T00:00:00Z",
          webhook_last_error_at: "2026-01-02T00:00:00Z",
          webhook_last_error_message: "unknown path",
        }),
      ),
    ).toMatchObject({ kind: "error", headline: "Delivery failed", message: "unknown path" });
    expect(
      sourceHealth(
        webhookSource({
          webhook_last_received_at: "2026-01-03T00:00:00Z",
          webhook_last_error_at: "2026-01-02T00:00:00Z",
        }),
      ),
    ).toMatchObject({ kind: "ok", headline: "Last delivery" });
  });

  it("separates a webhook with no endpoint from one with no deliveries", () => {
    expect(sourceHealth(webhookSource({ webhook_configured: false }))).toMatchObject({
      kind: "missing-endpoint",
    });
    expect(sourceHealth(webhookSource())).toMatchObject({
      kind: "idle",
      headline: "No deliveries yet",
    });
  });

  it("reports a poll error with its message", () => {
    expect(sourceHealth(pollSource({ last_error: "refused" }))).toMatchObject({
      kind: "error",
      headline: "Last poll failed",
      message: "refused",
    });
    expect(sourceHealth(pollSource())).toMatchObject({ kind: "idle", headline: "Not run yet" });
  });
});
