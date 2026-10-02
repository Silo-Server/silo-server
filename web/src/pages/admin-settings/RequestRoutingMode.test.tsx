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
  route,
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

const radarr4K = server("radarr-4k", "Radarr 4K", "radarr", {
  plugin_config: { service_kind: "radarr", is_4k: true },
});
const standard: Routing = {
  mode: "standard",
  standard: [
    { media_type: "movie", hd_integration_id: "radarr-1", uhd_integration_id: "radarr-4k" },
    { media_type: "series", hd_integration_id: "sonarr-1" },
  ],
};
const anime = route({
  id: "r-anime",
  name: "Anime",
  media_type: "series",
  conditions: { anime: true },
  hd: { integration_id: "sonarr-1" },
});

describe("Standard and Advanced routing", () => {
  it("says the rules decide while Standard is saved but two servers of a kind exist", async () => {
    serve({
      servers: [radarr, radarrAnime, sonarr],
      routing: {
        mode: "standard",
        standard: [],
        standard_unavailable_reason:
          "Movies can go to more than one server (Radarr, Radarr Anime).",
      },
    });
    mount();
    const group = await screen.findByRole("group", { name: "Where requests go" });
    expect(
      await within(group).findByText(/Standard can't route requests right now/),
    ).toHaveTextContent("Until you switch to Advanced or remove a server, the rules decide.");
  });

  it("says HD versions go nowhere when a media type has only a 4K server", async () => {
    serve({
      servers: [radarr4K, sonarr],
      routing: {
        mode: "standard",
        standard: [
          { media_type: "movie", uhd_integration_id: "radarr-4k" },
          { media_type: "series", hd_integration_id: "sonarr-1" },
        ],
      },
    });
    mount();
    const summary = await screen.findByRole("list", { name: "Where Standard sends requests" });
    expect(within(summary).getAllByRole("listitem")[0]).toHaveTextContent(
      "only a 4K server, so HD versions go nowhere",
    );
  });

  it("says where Standard sends each media type and keeps the rules out of the way", async () => {
    serve({
      servers: [radarr, radarr4K, sonarr],
      routes: [anime, fallback("movie", "radarr-1"), fallback("series", "sonarr-1")],
      routing: standard,
    });
    mount();
    const group = await screen.findByRole("group", { name: "Where requests go" });
    const summary = await within(group).findByRole("list", {
      name: "Where Standard sends requests",
    });
    const [movies, series] = within(summary).getAllByRole("listitem");
    expect(movies).toHaveTextContent("Movies → Radarr4K versions → Radarr 4K");
    expect(series).toHaveTextContent("Series → SonarrNo 4K versions");
    expect(group).toHaveTextContent("1 rule is paused. Switch to Advanced to use it again.");
    expect(group).toHaveTextContent("Anime series go to Sonarr with the Anime series type");
    // Series has no 4K server, so the page says how to add one.
    expect(group).toHaveTextContent("turn on “4K server”");
    expect(within(group).queryByRole("tab")).toBeNull();
    expect(within(group).queryByRole("button", { name: /Add a rule/ })).toBeNull();
    expect(within(group).getByRole("button", { name: /^Standard/ })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    // The server tiles say what Standard uses them for.
    const servers = screen.getByRole("group", { name: "Servers" });
    expect(servers).toHaveTextContent("4K movies");
  });

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

  it("says when adding a second server turned Advanced on", async () => {
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
    await waitFor(() =>
      expect(toast.success).toHaveBeenCalledWith(
        "Radarr Anime added. Routing is now Advanced, so you can choose which movies go to each server.",
      ),
    );
    expect(await screen.findByRole("tab", { name: "Movies" })).toBeInTheDocument();
  });
});
