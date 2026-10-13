/**
 * Shared fixtures for the Settings › Requests tests. The test files mock
 * `@/api/v2/request` (v2), the plugin installations and the admin users; this
 * module serves the page's reads from these fixtures through that mock.
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { vi } from "vitest";

import { v2, V2ProblemError } from "@/api/v2/request";

import RequestsSettings from "./RequestsSettings";

export { adminUsers, descriptor, pluginInstallations } from "./requestsSettings.mockData";

/** Radix Select opens through pointer capture and measures with ResizeObserver, which jsdom lacks. */
class ResizeObserverStub {
  observe() {}
  unobserve() {}
  disconnect() {}
}
export function stubBrowser() {
  vi.stubGlobal("ResizeObserver", ResizeObserverStub);
  window.HTMLElement.prototype.hasPointerCapture = () => false;
  window.HTMLElement.prototype.scrollIntoView = () => {};
  localStorage.clear();
}

export const settings = {
  requests_enabled: true,
  global_max_requests: 5,
  global_window_days: 7,
  global_auto_approval_enabled: false,
  force_dual_quality: false,
};

export function server(id: string, name: string, kind: "radarr" | "sonarr", extra = {}) {
  return {
    id,
    name,
    enabled: true,
    base_url: `http://${id}:7878`,
    has_api_key: true,
    installation_id: "1",
    capability_id: "arr",
    plugin_config: {
      service_kind: kind,
      quality_profile_id: 1,
      root_folder: kind === "radarr" ? "/movies" : "/tv",
      is_default: true,
      ...(kind === "sonarr" ? { series_type: "standard" } : {}),
    },
    supported_media_types: [kind === "radarr" ? "movie" : "series"],
    last_check_at: null,
    last_check_status: "",
    last_check_error: "",
    updated_at: "2026-09-05T00:00:00Z",
    ...extra,
  };
}
export const radarr = server("radarr-1", "Radarr", "radarr");
export const radarrAnime = server("radarr-2", "Radarr Anime", "radarr");
export const sonarr = server("sonarr-1", "Sonarr", "sonarr");
export const sonarrAnime = server("sonarr-2", "Sonarr Anime", "sonarr");

export type Route = {
  id: string;
  media_type: "movie" | "series";
  position: number;
  name: string;
  enabled: boolean;
  is_fallback: boolean;
  conditions: Record<string, unknown>;
  hd: { integration_id?: string; overrides?: Record<string, unknown> };
  uhd: { integration_id?: string; overrides?: Record<string, unknown> };
  skip_uhd: boolean;
};
export function route(extra: Partial<Route> & Pick<Route, "id">): Route {
  return {
    media_type: "movie",
    position: 0,
    name: extra.id,
    enabled: true,
    is_fallback: false,
    conditions: {},
    hd: {},
    uhd: {},
    skip_uhd: false,
    ...extra,
  };
}
export const fallback = (mediaType: "movie" | "series", hd?: string, uhd?: string) =>
  route({
    id: `fallback-${mediaType}`,
    media_type: mediaType,
    position: 1000,
    name: "Everything else",
    is_fallback: true,
    hd: hd ? { integration_id: hd } : {},
    uhd: uhd ? { integration_id: uhd } : {},
  });

export const serverOptions = {
  root_folder: [
    { value: "/movies", label: "/movies (1.2 TiB free)" },
    { value: "/anime", label: "/anime (300 GiB free)" },
  ],
  quality_profile_id: [
    { value: "1", label: "HD-1080p" },
    { value: "3", label: "Anime 1080p" },
  ],
  tags: [{ value: "2", label: "anime" }],
};

export function reply(options: unknown, body: unknown, etag = '"initial"') {
  (options as { onResponse?: (r: Response) => void })?.onResponse?.(
    new Response(null, { headers: { ETag: etag } }),
  );
  return Promise.resolve(body) as never;
}
export const problem = (status: number, type: string, detail: string, errors?: unknown[]) =>
  new V2ProblemError("test", {
    type: `https://silo.test/problems/${type}`,
    title: detail,
    status,
    detail,
    instance: "test",
    ...(errors ? { errors } : {}),
  } as never);
export const conflict = () => problem(412, "precondition_failed", "Changed");
export const invalid = (errors: { location: string; detail: string }[]) =>
  problem(
    422,
    "validation_failed",
    "The request did not pass validation; see errors.",
    errors.map((error) => ({ ...error, code: "invalid" })),
  );

export type Routing = {
  mode: "standard" | "advanced";
  standard: {
    media_type: "movie" | "series";
    hd_integration_id?: string;
    uhd_integration_id?: string;
  }[];
  standard_unavailable_reason?: string;
};
/** Advanced routing, with Standard unavailable: the routing tests' default. */
export const advancedRouting: Routing = {
  mode: "advanced",
  standard: [],
  standard_unavailable_reason: "Movies can go to more than one server (Radarr, Radarr Anime).",
};

export type Options = {
  path?: { id?: string };
  query?: Record<string, unknown>;
  body?: unknown;
  headers?: Record<string, string>;
};
export type Handler = (options: Options) => unknown;

/**
 * Serves the page's reads from fixtures and lets a test replace any
 * operation. Every route is read with an ETag naming its id, so a test can
 * tell which validator a write sent.
 */
export function serve({
  servers = [radarr, radarrAnime, sonarr],
  routes = [fallback("movie"), fallback("series", "sonarr-1")],
  requestSettings = settings,
  routing = advancedRouting,
  handlers = {},
}: {
  servers?: ReturnType<typeof server>[];
  routes?: Route[];
  requestSettings?: typeof settings;
  routing?: Routing;
  handlers?: Record<string, Handler>;
} = {}) {
  vi.mocked(v2).mockImplementation(((operation: string, options: Options) => {
    const custom = handlers[operation];
    if (custom) return custom(options);
    switch (operation) {
      case "GET /api/v2/admin/requests/capabilities":
        return reply(options, { available: true, guarded_configuration: true, routing: true });
      case "GET /api/v2/admin/request-settings":
        return reply(options, requestSettings);
      case "GET /api/v2/admin/request-integrations":
        return reply(options, { items: servers, page: { has_more: false } });
      case "GET /api/v2/admin/request-integrations/{id}":
        return reply(
          options,
          servers.find((s) => s.id === options.path?.id),
        );
      case "POST /api/v2/admin/request-integrations/{id}/options":
        return reply(options, { options: serverOptions });
      case "GET /api/v2/admin/request-routing":
        return reply(options, routing, '"routing-v1"');
      case "GET /api/v2/admin/request-routes":
        return reply(options, { items: routes });
      case "GET /api/v2/admin/request-routes/{id}": {
        const found = routes.find((r) => r.id === options.path?.id);
        // Everything else is at revision zero until its first save.
        const etag =
          found?.is_fallback && !found.hd.integration_id ? '"0"' : `"${options.path?.id}-v1"`;
        return reply(options, found, etag);
      }
      case "GET /api/v2/requests/discover/networks":
      case "GET /api/v2/requests/discover/studios":
        return reply(options, { items: [] });
      default:
        return Promise.reject(new Error(`unexpected ${operation}`));
    }
  }) as never);
}

export function calls(operation: string) {
  return vi
    .mocked(v2)
    .mock.calls.filter(([op]) => op === operation)
    .map(([, options]) => options as Options);
}

export function mount() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={["/admin/settings/requests"]}>
        <RequestsSettings />
      </MemoryRouter>
    </QueryClientProvider>,
  );
  return client;
}

export const group = (name: string) => screen.getByRole("group", { name });
export const user = () => userEvent.setup({ pointerEventsCheck: 0 });

export async function choose(scope: HTMLElement, label: string, option: string) {
  await user().click(await within(scope).findByRole("combobox", { name: label }));
  await user().click(await screen.findByRole("option", { name: option }));
}

/** A promise the test settles when it chooses. */
export function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((r) => {
    resolve = r;
  });
  return { promise, resolve };
}
