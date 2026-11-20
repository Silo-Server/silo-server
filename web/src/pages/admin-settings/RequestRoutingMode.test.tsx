// @vitest-environment jsdom
import { cleanup, fireEvent, screen, waitFor, within } from "@testing-library/react";
import { toast } from "sonner";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  calls,
  choose,
  fallback,
  mount,
  radarr,
  radarrAnime,
  reply,
  serve,
  server,
  sonarr,
  stubBrowser,
  user,
  type Routing,
} from "./requestsSettings.fixtures";
vi.mock("@/api/v2/request", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/api/v2/request")>()),
  v2: vi.fn(),
}));
vi.mock("@/api/client", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/api/client")>()),
  captureProfileRequestContext: () => ({
    accessToken: "test",
    profileId: "profile",
    profileToken: null,
    authContextVersion: 1,
    serverOrigin: "",
  }),
  isProfileRequestContextCurrent: () => true,
}));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));
vi.mock("@/hooks/queries/admin/users", async () => {
  const { adminUsers } = await import("./requestsSettings.mockData");
  return { useAdminUsers: () => adminUsers };
});
vi.mock("@/hooks/queries/admin/plugins", async () => {
  const { pluginInstallations } = await import("./requestsSettings.mockData");
  return { useAdminPluginInstallations: () => pluginInstallations };
});

beforeEach(stubBrowser);
afterEach(() => {
  cleanup();
  vi.clearAllMocks();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});
const standard: Routing = {
  mode: "standard",
  standard: [
    { media_type: "movie", hd_integration_id: "radarr-1", uhd_integration_id: "radarr-4k" },
    { media_type: "series", hd_integration_id: "sonarr-1" },
  ],
};

describe("Standard and Advanced routing", () => {
  it("switches to Advanced with the routing's validator and shows the rules", async () => {
    serve({
      servers: [radarr, sonarr],
      routes: [fallback("movie", "radarr-1"), fallback("series", "sonarr-1")],
      routing: standard,
      handlers: {
        "PUT /api/v2/admin/request-routing": (options) =>
          reply(options, { ...standard, mode: "advanced" }, '"routing-v2"'),
      },
    });
    mount();
    const group = await screen.findByRole("group", { name: "Where requests go" });
    await user().click(await within(group).findByRole("button", { name: /^Advanced/ }));
    await waitFor(() => expect(calls("PUT /api/v2/admin/request-routing")).toHaveLength(1));
    const [options] = calls("PUT /api/v2/admin/request-routing") as [
      { body: unknown; headers: Record<string, string> },
    ];
    expect(options.body).toEqual({ mode: "advanced" });
    expect(options.headers["If-Match"]).toBe('"routing-v1"');
    expect(await within(group).findByRole("tab", { name: "Movies" })).toBeInTheDocument();
    expect(toast.success).toHaveBeenCalledWith("Advanced routing is on");
  });

  it("explains why Standard is unavailable", async () => {
    serve({ servers: [radarr, radarrAnime, sonarr] });
    mount();
    const group = await screen.findByRole("group", { name: "Where requests go" });
    const choice = await within(group).findByRole("button", { name: /^Standard/ });
    expect(choice).toHaveAttribute("aria-disabled", "true");
    expect(choice).toHaveAccessibleDescription(
      "Standard isn't available. Movies can go to more than one server (Radarr, Radarr Anime).",
    );
    await user().click(choice);
    expect(calls("PUT /api/v2/admin/request-routing")).toHaveLength(0);
  });

  it("marks a server as the 4K one and turns the older 4K default off with it", async () => {
    const legacy = server("radarr-4k", "Radarr 4K", "radarr", {
      plugin_config: { service_kind: "radarr", is_4k: true, is_default_4k: true },
    });
    serve({
      servers: [radarr, legacy, sonarr],
      routing: standard,
      handlers: {
        "PUT /api/v2/admin/request-integrations/{id}": (options) =>
          reply(options, legacy, '"saved"'),
      },
    });
    mount();
    const servers = await screen.findByRole("group", { name: "Servers" });
    const tiles = await within(servers).findAllByRole("button", { name: "Edit" });
    await user().click(tiles[1]!);
    const dialog = await screen.findByRole("dialog");
    const fourK = within(dialog).getByRole("switch", { name: "4K server" });
    expect(fourK).toBeChecked();
    await user().click(fourK);
    await user().click(within(dialog).getByRole("button", { name: "Save" }));
    await waitFor(() =>
      expect(calls("PUT /api/v2/admin/request-integrations/{id}")).toHaveLength(1),
    );
    const [options] = calls("PUT /api/v2/admin/request-integrations/{id}") as [
      { body: { plugin_config: Record<string, unknown> } },
    ];
    expect(options.body.plugin_config.is_4k).toBe(false);
    // Off or gone: the older switch no longer marks it 4K either.
    expect(options.body.plugin_config.is_default_4k).not.toBe(true);
  });

  it("refreshes routing after adding a second server", async () => {
    const added = server("radarr-9", "Radarr Anime", "radarr");
    let created = false;
    serve({
      servers: [radarr, sonarr],
      routing: standard,
      handlers: {
        "POST /api/v2/admin/request-integrations": (options) => {
          created = true;
          return reply(options, added, '"new"');
        },
        "GET /api/v2/admin/request-routing": (options) =>
          reply(options, created ? { ...standard, mode: "advanced" } : standard, '"routing-v1"'),
      },
    });
    mount();
    await screen.findByRole("list", { name: "Where Standard sends requests" });
    fireEvent.click(await screen.findByRole("button", { name: "Add server" }));
    const dialog = await screen.findByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText("Name"), { target: { value: "Radarr Anime" } });
    fireEvent.change(within(dialog).getByLabelText("URL"), {
      target: { value: "http://radarr-anime:7878" },
    });
    fireEvent.change(within(dialog).getByLabelText("API key"), { target: { value: "key" } });
    await choose(dialog, "Service", "Radarr (movies)");
    fireEvent.click(within(dialog).getByRole("button", { name: "Add server" }));
    await waitFor(() => expect(calls("POST /api/v2/admin/request-integrations")).toHaveLength(1));
    expect(await screen.findByRole("tab", { name: "Movies" })).toBeInTheDocument();
  });
});
