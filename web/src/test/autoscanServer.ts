/**
 * An in-memory autoscan admin API for SourcesPanel tests. Replaces the global
 * `fetch`; reads come from the supplied state and writes are recorded so a
 * test can assert the exact request body. Write handlers echo the request
 * back as the stored source unless a test overrides them.
 */
import { vi } from "vitest";

import type {
  AutoscanAvailableSource,
  AutoscanConnection,
  AutoscanScanSourceDescriptor,
  AutoscanSource,
} from "@/api/types";

export interface RecordedWrite {
  method: string;
  path: string;
  body: Record<string, unknown> | null;
}

export interface AutoscanServerState {
  sources: AutoscanSource[];
  plugins: AutoscanAvailableSource[];
  connections?: AutoscanConnection[];
  defaultPollSeconds?: number;
  /** Optional override for any request; return undefined to fall through. */
  handle?: (
    method: string,
    path: string,
    body: unknown,
  ) => Response | Promise<Response> | undefined;
}

export const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });

export function stubAutoscanServer(state: AutoscanServerState) {
  const writes: RecordedWrite[] = [];
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(String(input instanceof Request ? input.url : input), "http://test");
    const method = (
      init?.method ?? (input instanceof Request ? input.method : "GET")
    ).toUpperCase();
    const path = url.pathname;
    const body = typeof init?.body === "string" ? JSON.parse(init.body) : null;
    if (method !== "GET") writes.push({ method, path, body });

    const override = state.handle?.(method, path, body);
    if (override) return override;

    if (path.endsWith("/admin/autoscan/sources") && method === "GET")
      return json({ items: state.sources, page: { has_more: false } });
    if (path.endsWith("/admin/autoscan/sources") && method === "POST")
      return json({ ...body, id: "created-source", webhook_configured: false }, 201);
    const sourceWrite = path.match(/\/admin\/autoscan\/sources\/([^/]+)$/);
    if (sourceWrite && method === "PUT") {
      const existing = state.sources.find((s) => s.id === sourceWrite[1]);
      return json({ ...existing, ...body });
    }
    if (path.endsWith("/admin/autoscan/connections"))
      return json({ items: state.connections ?? [], page: { has_more: false } });
    if (path.endsWith("/admin/autoscan/settings"))
      return json({
        enabled: true,
        default_poll_interval_seconds: state.defaultPollSeconds ?? 600,
        debounce_seconds: 60,
      });
    if (path.endsWith("/admin/autoscan/scan-source-plugins"))
      return json({ items: state.plugins, page: { has_more: false } });
    return json({ title: "unexpected request" }, 404);
  });
  vi.stubGlobal("fetch", fetchMock);
  return { fetchMock, writes };
}

export const ARR_WEBHOOK_PLUGIN: AutoscanAvailableSource = {
  plugin_id: "silo.autoscan.arr-webhook",
  capability_id: "arr-webhook",
  display_name: "Sonarr/Radarr Webhook",
  description: "Sonarr or Radarr posts to Silo the moment an import finishes.",
  descriptor: {
    delivery_modes: ["webhook"],
    connection: "none",
    connection_kinds: ["sonarr", "radarr"],
    emits_native_paths: false,
    summary: "No API key needed.",
    icon_url: "",
    config_form: {
      fields: [
        {
          key: "webhook_provider",
          label: "Provider",
          control: "SELECT",
          required: false,
          secret: false,
          multiline: false,
          options: [
            { value: "auto", label: "Detect automatically" },
            { value: "sonarr", label: "Sonarr" },
            { value: "radarr", label: "Radarr" },
          ],
        },
      ],
    },
  },
};

export const ARR_POLL_PLUGIN: AutoscanAvailableSource = {
  plugin_id: "silo.autoscan.arr",
  capability_id: "arr",
  display_name: "Sonarr / Radarr",
  description: "Triggers Silo rescans from Sonarr/Radarr import and rename history.",
  descriptor: {
    delivery_modes: ["poll"],
    connection: "optional",
    connection_kinds: ["sonarr", "radarr"],
    emits_native_paths: false,
    summary: "",
    icon_url: "",
    config_form: {
      fields: [
        {
          key: "lookback",
          label: "Lookback",
          control: "TEXT",
          required: false,
          secret: false,
          multiline: false,
        },
      ],
    },
  },
};

/** A copy of a fixture plugin with parts of its descriptor replaced. */
export function withDescriptor(
  plugin: AutoscanAvailableSource,
  patch: Partial<AutoscanScanSourceDescriptor>,
): AutoscanAvailableSource {
  return { ...plugin, descriptor: { ...plugin.descriptor!, ...patch } };
}

export function pollSource(overrides: Partial<AutoscanSource> = {}): AutoscanSource {
  return {
    id: "poll-a",
    plugin_id: "silo.autoscan.arr",
    capability_id: "arr",
    connection_id: "conn-sonarr",
    enabled: true,
    delivery_mode: "poll",
    poll_interval_seconds: null,
    path_rewrites: [{ from: "/data/tv", to: "/mnt/media/tv" }],
    source_config: { lookback: "24h" },
    label: "",
    last_run_at: null,
    last_error: null,
    webhook_configured: false,
    ...overrides,
  };
}

export function webhookSource(overrides: Partial<AutoscanSource> = {}): AutoscanSource {
  return {
    id: "hook-a",
    plugin_id: "silo.autoscan.arr-webhook",
    capability_id: "arr-webhook",
    connection_id: null,
    enabled: true,
    delivery_mode: "webhook",
    poll_interval_seconds: null,
    path_rewrites: [{ from: "/tv", to: "/mnt/media/tv" }],
    source_config: { webhook_provider: "sonarr" },
    label: "",
    last_run_at: null,
    last_error: null,
    webhook_configured: true,
    webhook_url: "/api/v2/autoscan/webhooks/synthetic-secret",
    ...overrides,
  };
}

export const SONARR_CONNECTION: AutoscanConnection = {
  id: "conn-sonarr",
  name: "Sonarr 4K",
  kind: "sonarr",
  base_url: "http://sonarr.invalid",
  has_api_key: true,
};
