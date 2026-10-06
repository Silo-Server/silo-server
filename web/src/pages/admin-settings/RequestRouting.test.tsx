// @vitest-environment jsdom
import { act, cleanup, fireEvent, screen, waitFor, within } from "@testing-library/react";
import { toast } from "sonner";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  calls,
  choose,
  conflict,
  deferred,
  fallback,
  invalid,
  mount,
  radarr,
  radarrAnime,
  reply,
  route,
  serve,
  server,
  settings,
  sonarr,
  sonarrAnime,
  stubBrowser,
  user,
  type Route,
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

const allServers = [radarr, radarrAnime, sonarr, sonarrAnime];
const ready = [fallback("movie", "radarr-1"), fallback("series", "sonarr-1")];

/** The routing section, on the given tab. */
async function section(tab: "Movies" | "Series" = "Movies") {
  const group = await screen.findByRole("group", { name: "Where requests go" });
  const trigger = await within(group).findByRole("tab", { name: tab });
  if (tab === "Series") await user().click(trigger);
  await waitFor(() =>
    expect(within(group).getByRole("tabpanel")).toHaveTextContent(/Everything else|server above/),
  );
  return group;
}

/** The rule names in the list, top to bottom. */
function ruleOrder(scope: HTMLElement) {
  const list = within(scope).getByRole("list", { name: /rules$/ });
  return within(list)
    .getAllByRole("switch")
    .map((toggle) => toggle.getAttribute("aria-label")!.replace(/ enabled$/, ""));
}

async function openMenu(scope: HTMLElement, rule: string) {
  await user().click(within(scope).getByRole("button", { name: `More for ${rule}` }));
  return screen.findByRole("menu");
}

const created = (options: { body?: unknown }) =>
  reply(options, { ...route({ id: "new" }), ...(options.body as object) }, '"new-v1"');

describe("Where requests go: the list", () => {
  it("explains an empty media type, and offers Add a rule only once Everything else is saved", async () => {
    serve({ servers: [sonarr], routes: [fallback("movie"), fallback("series", "sonarr-1")] });
    mount();
    const movies = await section();
    expect(
      within(movies).getByText("Add a Radarr server above to send movie requests anywhere."),
    ).toBeInTheDocument();

    const series = await section("Series");
    expect(
      within(series).getByText(
        "Every series request goes to Sonarr. Add a rule only if some series should go somewhere else, like anime or kids' titles.",
      ),
    ).toBeInTheDocument();
    expect(within(series).getByRole("button", { name: "Add a rule" })).toBeEnabled();

    cleanup();
    serve();
    mount();
    const unsaved = await section();
    expect(within(unsaved).getByRole("button", { name: "Add a rule" })).toBeDisabled();
    expect(
      within(unsaved).getByText("Choose where everything else goes first."),
    ).toBeInTheDocument();
  });

  it("switches a rule on and off, and says why a switch was refused", async () => {
    serve({
      routes: [route({ id: "a", name: "First", conditions: { anime: true } }), ...ready],
      handlers: {
        "PUT /api/v2/admin/request-routes/{id}": () =>
          Promise.reject(
            invalid([
              { location: "body.hd.integration_id", detail: "That server no longer exists." },
            ]),
          ),
      },
    });
    mount();
    const movies = await section();
    fireEvent.click(await within(movies).findByRole("switch", { name: "First enabled" }));
    await waitFor(() => expect(calls("PUT /api/v2/admin/request-routes/{id}")).toHaveLength(1));
    expect(calls("PUT /api/v2/admin/request-routes/{id}")[0]).toMatchObject({
      path: { id: "a" },
      headers: { "If-Match": '"a-v1"' },
      body: { enabled: false, conditions: { anime: true } },
    });
    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith(
        "The request did not pass validation; see errors. That server no longer exists.",
      ),
    );
  });

  it("moves rules from the menu, sending every rule in the new order, and holds the list until it is read back", async () => {
    let routes: Route[] = [
      route({ id: "a", name: "First", position: 0, conditions: { anime: true } }),
      route({ id: "b", name: "Second", position: 1, conditions: { genre_ids: [27] } }),
      ...ready,
    ];
    let hold: ReturnType<typeof deferred<void>> | null = null;
    serve({
      handlers: {
        "GET /api/v2/admin/request-routes": async (options) => {
          if (hold) await hold.promise;
          return reply(options, { items: routes });
        },
        "GET /api/v2/admin/request-routes/{id}": (options) =>
          reply(
            options,
            routes.find((r) => r.id === options.path?.id),
            '"v"',
          ),
        "POST /api/v2/admin/request-routes/order": (options) => {
          const ids = (options.body as { ids: string[] }).ids;
          routes = routes.map((r) =>
            ids.includes(r.id) ? { ...r, position: ids.indexOf(r.id) } : r,
          );
          hold = deferred<void>();
          return reply(options, { items: [] });
        },
      },
    });
    mount();
    const movies = await section();
    await within(movies).findByRole("switch", { name: "Second enabled" });
    const menu = await openMenu(movies, "Second");
    expect(within(menu).getByRole("menuitem", { name: "Move down" })).toHaveAttribute(
      "aria-disabled",
      "true",
    );
    await user().click(within(menu).getByRole("menuitem", { name: "Move up" }));
    await waitFor(() => expect(calls("POST /api/v2/admin/request-routes/order")).toHaveLength(1));
    expect(calls("POST /api/v2/admin/request-routes/order")[0]!.body).toEqual({
      media_type: "movie",
      ids: ["b", "a"],
    });

    // The new order shows at once, but its validators are not read back yet,
    // so nothing may be computed from it until they are.
    expect(ruleOrder(movies)).toEqual(["Second", "First"]);
    await waitFor(() =>
      expect(within(movies).getByRole("switch", { name: "First enabled" })).toBeDisabled(),
    );
    expect(within(movies).getByRole("button", { name: "Drag First" })).toBeDisabled();

    await act(async () => hold!.resolve());
    await waitFor(() =>
      expect(within(movies).getByRole("switch", { name: "First enabled" })).toBeEnabled(),
    );
    expect(ruleOrder(movies)).toEqual(["Second", "First"]);
  });

  it("puts the order back when a reorder is refused", async () => {
    const held = deferred<void>();
    serve({
      routes: [
        route({ id: "a", name: "First", position: 0, conditions: { anime: true } }),
        route({ id: "b", name: "Second", position: 1, conditions: { genre_ids: [27] } }),
        ...ready,
      ],
      handlers: {
        "POST /api/v2/admin/request-routes/order": async () => {
          await held.promise;
          throw invalid([{ location: "body.ids", detail: "Reload and try again." }]);
        },
      },
    });
    mount();
    const movies = await section();
    await within(movies).findByRole("switch", { name: "Second enabled" });
    await user().click(
      within(await openMenu(movies, "Second")).getByRole("menuitem", { name: "Move up" }),
    );
    await waitFor(() => expect(ruleOrder(movies)).toEqual(["Second", "First"]));
    await act(async () => held.resolve());
    await waitFor(() => expect(ruleOrder(movies)).toEqual(["First", "Second"]));
    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith(
        "The request did not pass validation; see errors. Reload and try again.",
      ),
    );
  });

  it("deletes a rule from the menu after asking", async () => {
    serve({
      routes: [route({ id: "a", name: "First", conditions: { anime: true } }), ...ready],
      handlers: {
        "DELETE /api/v2/admin/request-routes/{id}": (options) => reply(options, undefined),
      },
    });
    mount();
    const movies = await section();
    await within(movies).findByRole("switch", { name: "First enabled" });
    await user().click(
      within(await openMenu(movies, "First")).getByRole("menuitem", { name: "Delete" }),
    );
    const confirm = await screen.findByRole("alertdialog");
    fireEvent.click(within(confirm).getByRole("button", { name: "Delete" }));
    await waitFor(() => expect(calls("DELETE /api/v2/admin/request-routes/{id}")).toHaveLength(1));
    expect(calls("DELETE /api/v2/admin/request-routes/{id}")[0]).toMatchObject({
      path: { id: "a" },
      headers: { "If-Match": '"a-v1"' },
    });
    await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
    // The menu that opened it leaves the page clickable.
    expect(document.body.style.pointerEvents).toBe("");
  });

  it("closes the delete confirmation after a 412 and reads the list again", async () => {
    serve({
      routes: [route({ id: "a", name: "First", conditions: { anime: true } }), ...ready],
      handlers: {
        "DELETE /api/v2/admin/request-routes/{id}": () => Promise.reject(conflict()),
      },
    });
    mount();
    const movies = await section();
    await within(movies).findByRole("switch", { name: "First enabled" });
    await user().click(
      within(await openMenu(movies, "First")).getByRole("menuitem", { name: "Delete" }),
    );
    fireEvent.click(
      within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Delete" }),
    );
    await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
    await waitFor(() => expect(calls("GET /api/v2/admin/request-routes")).toHaveLength(2));
    expect(toast.error).toHaveBeenCalledWith("Changed");
    expect(document.body.style.pointerEvents).toBe("");
  });

  it("opens the editor from the menu and leaves the page clickable after closing it", async () => {
    serve({ routes: [route({ id: "a", name: "First", conditions: { anime: true } }), ...ready] });
    mount();
    const movies = await section();
    await within(movies).findByRole("switch", { name: "First enabled" });
    await user().click(
      within(await openMenu(movies, "First")).getByRole("menuitem", { name: "Edit" }),
    );
    const dialog = await screen.findByRole("dialog");
    await user().click(within(dialog).getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    await waitFor(() => expect(document.body.style.pointerEvents).toBe(""));
  });
});

describe("Where requests go: Everything else", () => {
  it("saves a never-saved Everything else as soon as a server is chosen, with the revision-zero validator", async () => {
    serve({
      handlers: {
        "PUT /api/v2/admin/request-routes/{id}": (options) =>
          reply(options, fallback("movie", "radarr-1"), '"fallback-movie-v2"'),
      },
    });
    mount();
    const movies = await section();
    await choose(movies, "Everything else server", "Radarr");
    await waitFor(() => expect(calls("PUT /api/v2/admin/request-routes/{id}")).toHaveLength(1));
    expect(calls("PUT /api/v2/admin/request-routes/{id}")[0]).toEqual(
      expect.objectContaining({
        path: { id: "fallback-movie" },
        headers: { "If-Match": '"0"' },
        body: {
          name: "Everything else",
          enabled: true,
          conditions: {},
          hd: { integration_id: "radarr-1" },
          uhd: {},
          skip_uhd: false,
        },
      }),
    );
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith("Everything else saved"));
  });

  it("offers only HD servers for a never-saved Everything else", async () => {
    const radarr4K = server("radarr-4k", "Radarr 4K", "radarr", {
      plugin_config: { service_kind: "radarr", quality_profile_id: 1, is_4k: true },
    });
    serve({ servers: [radarr, radarr4K, sonarr] });
    mount();
    const movies = await section();
    await user().click(
      await within(movies).findByRole("combobox", { name: "Everything else server" }),
    );
    expect((await screen.findAllByRole("option")).map((option) => option.textContent)).toEqual([
      "Radarr",
    ]);
  });

  it("offers only HD servers for the HD version and only 4K servers for the 4K version", async () => {
    const radarr4K = server("radarr-4k", "Radarr 4K", "radarr", {
      plugin_config: { service_kind: "radarr", quality_profile_id: 1, is_4k: true },
    });
    // A 4K version saved before the rule stays visible, saying why it no longer fits.
    const stored = { ...fallback("movie", "radarr-1"), uhd: { integration_id: radarrAnime.id } };
    serve({
      servers: [radarr, radarrAnime, radarr4K, sonarr],
      routes: [stored, fallback("series", "sonarr-1")],
      handlers: {
        "GET /api/v2/admin/request-routes/{id}": (options) =>
          reply(
            options,
            options.path?.id === "fallback-movie" ? stored : fallback("series", "sonarr-1"),
            '"v1"',
          ),
      },
    });
    mount();
    const movies = await section();
    fireEvent.click(
      within(await within(movies).findByRole("group", { name: "Everything else" })).getByRole(
        "button",
      ),
    );
    const dialog = await screen.findByRole("dialog");
    const optionNames = async (label: string) => {
      await user().click(await within(dialog).findByRole("combobox", { name: label }));
      const names = (await screen.findAllByRole("option")).map((option) => option.textContent);
      await user().keyboard("{Escape}");
      return names;
    };

    expect(await optionNames("HD version Send to")).toEqual([radarr.name, radarrAnime.name]);
    expect(await optionNames("4K version Send to")).toEqual([
      "Radarr 4K",
      `${radarrAnime.name} (not marked 4K)`,
      "Don't send a 4K version",
    ]);
  });

  it("offers a server of another plugin for both versions", async () => {
    // Seerr has no 4K switch of ours and handles both versions itself.
    const seerr = {
      ...server("seerr-1", "Seerr", "radarr"),
      capability_id: "seerr",
      plugin_config: {},
      supported_media_types: ["movie"],
    } as unknown as ReturnType<typeof server>;
    const stored = fallback("movie", "radarr-1");
    serve({
      servers: [radarr, seerr, sonarr],
      routes: [stored, fallback("series", "sonarr-1")],
      handlers: {
        "GET /api/v2/admin/request-routes/{id}": (options) =>
          reply(
            options,
            options.path?.id === "fallback-movie" ? stored : fallback("series", "sonarr-1"),
            '"v1"',
          ),
      },
    });
    mount();
    const movies = await section();
    fireEvent.click(
      within(await within(movies).findByRole("group", { name: "Everything else" })).getByRole(
        "button",
      ),
    );
    const dialog = await screen.findByRole("dialog");
    await user().click(await within(dialog).findByRole("combobox", { name: "4K version Send to" }));
    expect((await screen.findAllByRole("option")).map((option) => option.textContent)).toEqual([
      "Seerr",
      "Don't send a 4K version",
    ]);
    expect(within(dialog).queryByText(/No server is marked 4K/)).toBeNull();
  });

  it("stops making 4K copies from its editor, and reloads after a 412", async () => {
    let reads = 0;
    const stored = { ...fallback("movie", "radarr-1"), uhd: { integration_id: "radarr-2" } };
    serve({
      routes: [stored, fallback("series", "sonarr-1")],
      handlers: {
        "GET /api/v2/admin/request-routes/{id}": (options) => {
          if (options.path?.id !== "fallback-movie") {
            return reply(options, fallback("series", "sonarr-1"), '"s"');
          }
          reads += 1;
          return reply(options, stored, reads === 1 ? '"initial"' : '"reloaded"');
        },
        "PUT /api/v2/admin/request-routes/{id}": (options) =>
          options.headers?.["If-Match"] === '"initial"'
            ? Promise.reject(conflict())
            : reply(options, fallback("movie", "radarr-1"), '"saved"'),
      },
    });
    mount();
    const movies = await section();
    fireEvent.click(
      within(await within(movies).findByRole("group", { name: "Everything else" })).getByRole(
        "button",
      ),
    );
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByRole("heading", { name: "Everything else — movies" })).toBeTruthy();
    expect(
      within(dialog).getByText(/^Where movies go when no rule matches them\./),
    ).toBeInTheDocument();
    await choose(dialog, "4K version Send to", "Don't send a 4K version");
    fireEvent.click(within(dialog).getByRole("button", { name: "Save" }));
    await within(dialog).findByRole("button", { name: "Reload latest version" });
    expect(calls("PUT /api/v2/admin/request-routes/{id}")[0]).toMatchObject({
      headers: { "If-Match": '"initial"' },
      body: { hd: { integration_id: "radarr-1" }, uhd: {}, skip_uhd: false },
    });

    fireEvent.click(within(dialog).getByRole("button", { name: "Reload latest version" }));
    await waitFor(() =>
      expect(within(dialog).queryByRole("button", { name: "Reload latest version" })).toBeNull(),
    );
    await choose(dialog, "4K version Send to", "Don't send a 4K version");
    fireEvent.click(within(dialog).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(calls("PUT /api/v2/admin/request-routes/{id}")).toHaveLength(2));
    expect(calls("PUT /api/v2/admin/request-routes/{id}")[1]).toMatchObject({
      headers: { "If-Match": '"reloaded"' },
      body: { uhd: {} },
    });
  });

  it("opens More settings when a reload brings one of them in", async () => {
    let reads = 0;
    const reloaded = {
      ...fallback("series", "sonarr-1"),
      hd: { integration_id: "sonarr-1", overrides: { series_type: "anime" } },
    };
    serve({
      routes: [fallback("movie", "radarr-1"), fallback("series", "sonarr-1")],
      handlers: {
        "GET /api/v2/admin/request-routes/{id}": (options) => {
          if (options.path?.id !== "fallback-series") {
            return reply(options, fallback("movie", "radarr-1"), '"m"');
          }
          reads += 1;
          return reads === 1
            ? reply(options, fallback("series", "sonarr-1"), '"initial"')
            : reply(options, reloaded, '"reloaded"');
        },
        "PUT /api/v2/admin/request-routes/{id}": () => Promise.reject(conflict()),
      },
    });
    mount();
    const series = await section("Series");
    fireEvent.click(
      within(await within(series).findByRole("group", { name: "Everything else" })).getByRole(
        "button",
      ),
    );
    const dialog = await screen.findByRole("dialog");
    const more = await within(dialog).findByRole("button", { name: /^More settings/ });
    expect(more).toHaveAttribute("aria-expanded", "false");
    await choose(dialog, "4K version Send to", "Don't send a 4K version");
    fireEvent.click(within(dialog).getByRole("button", { name: "Save" }));
    fireEvent.click(await within(dialog).findByRole("button", { name: "Reload latest version" }));

    await waitFor(() =>
      expect(within(dialog).getByRole("button", { name: /^More settings/ })).toHaveAttribute(
        "aria-expanded",
        "true",
      ),
    );
    expect(within(dialog).getByRole("button", { name: /^More settings/ })).toHaveTextContent(
      "1 changed",
    );
  });
});

describe("Where requests go: adding rules", () => {
  async function pick(tab: "Movies" | "Series", preset: string) {
    const scope = await section(tab);
    fireEvent.click(within(scope).getByRole("button", { name: "Add a rule" }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText("Start from a common rule or build your own.")).toBeTruthy();
    fireEvent.click(within(dialog).getByRole("button", { name: new RegExp(`^${preset}`) }));
    return dialog;
  }

  it("adds the Anime preset for series and sets Sonarr's series type to Anime", async () => {
    serve({
      servers: allServers,
      routes: ready,
      handlers: { "POST /api/v2/admin/request-routes": created },
    });
    mount();
    const dialog = await pick("Series", "Anime");
    expect(
      within(dialog).getByRole("heading", { name: "Where should anime series go?" }),
    ).toBeTruthy();
    expect(within(dialog).getByText(/Matches: series that are anime/)).toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: "Add rule" })).toBeDisabled();
    await choose(dialog, "HD version Send to", "Sonarr Anime");
    expect(
      within(dialog).getByText("Series type: Anime (set by the Anime preset)"),
    ).toBeInTheDocument();
    await choose(dialog, "Folder", "/anime (300 GiB free)");
    fireEvent.click(within(dialog).getByRole("button", { name: "Add rule" }));

    await waitFor(() => expect(calls("POST /api/v2/admin/request-routes")).toHaveLength(1));
    expect(calls("POST /api/v2/admin/request-routes")[0]!.body).toEqual({
      media_type: "series",
      name: "Anime",
      enabled: true,
      conditions: { anime: true },
      hd: {
        integration_id: "sonarr-2",
        overrides: { series_type: "anime", root_folder: "/anime" },
      },
      uhd: {},
      skip_uhd: false,
    });
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith("Rule added"));
  });

  it("adds the Anime preset for movies without a series type", async () => {
    serve({
      servers: allServers,
      routes: ready,
      handlers: { "POST /api/v2/admin/request-routes": created },
    });
    mount();
    const dialog = await pick("Movies", "Anime");
    await choose(dialog, "HD version Send to", "Radarr Anime");
    fireEvent.click(within(dialog).getByRole("button", { name: "Add rule" }));
    await waitFor(() => expect(calls("POST /api/v2/admin/request-routes")).toHaveLength(1));
    expect(calls("POST /api/v2/admin/request-routes")[0]!.body).toEqual({
      media_type: "movie",
      name: "Anime",
      enabled: true,
      conditions: { anime: true },
      hd: { integration_id: "radarr-2" },
      uhd: {},
      skip_uhd: false,
    });
  });

  it("adds Foreign language in the admin's language, with no 4K copy", async () => {
    vi.spyOn(navigator, "language", "get").mockReturnValue("fr-FR");
    serve({ routes: ready, handlers: { "POST /api/v2/admin/request-routes": created } });
    mount();
    const movies = await section();
    fireEvent.click(within(movies).getByRole("button", { name: "Add a rule" }));
    const cards = await screen.findByRole("dialog");
    expect(within(cards).getByText("Titles not originally in French.")).toBeInTheDocument();
    fireEvent.click(within(cards).getByRole("button", { name: /^Foreign language/ }));
    const dialog = await screen.findByRole("dialog");
    expect(
      within(dialog).getByRole("heading", { name: "Where should foreign-language movies go?" }),
    ).toBeTruthy();
    await choose(dialog, "HD version Send to", "Radarr Anime");
    await choose(dialog, "4K version Send to", "Don't send a 4K version");
    fireEvent.click(within(dialog).getByRole("button", { name: "Add rule" }));
    await waitFor(() => expect(calls("POST /api/v2/admin/request-routes")).toHaveLength(1));
    expect(calls("POST /api/v2/admin/request-routes")[0]!.body).toEqual({
      media_type: "movie",
      name: "Foreign language",
      enabled: true,
      conditions: { exclude_original_languages: ["fr"] },
      hd: { integration_id: "radarr-2" },
      uhd: {},
      skip_uhd: true,
    });
  });

  it.each([
    ["Movies", "movie", [10751]],
    ["Series", "series", [10751, 10762]],
  ] as const)("adds Kids & family for %s", async (tab, mediaType, genres) => {
    serve({
      servers: allServers,
      routes: ready,
      handlers: { "POST /api/v2/admin/request-routes": created },
    });
    mount();
    const dialog = await pick(tab, "Kids & family");
    await choose(
      dialog,
      "HD version Send to",
      mediaType === "movie" ? "Radarr Anime" : "Sonarr Anime",
    );
    fireEvent.click(within(dialog).getByRole("button", { name: "Add rule" }));
    await waitFor(() => expect(calls("POST /api/v2/admin/request-routes")).toHaveLength(1));
    expect(calls("POST /api/v2/admin/request-routes")[0]!.body).toEqual({
      media_type: mediaType,
      name: "Kids & family",
      enabled: true,
      conditions: { genre_ids: genres, max_content_rating: "PG" },
      hd: { integration_id: mediaType === "movie" ? "radarr-2" : "sonarr-2" },
      uhd: {},
      skip_uhd: false,
    });
  });

  it("marks a preset already in the list, and lets its conditions change", async () => {
    serve({
      routes: [
        route({
          id: "a",
          name: "Anime",
          conditions: { anime: true },
          hd: { integration_id: "radarr-2" },
        }),
        ...ready,
      ],
      handlers: { "POST /api/v2/admin/request-routes": created },
    });
    mount();
    const movies = await section();
    await within(movies).findByRole("switch", { name: "Anime enabled" });
    fireEvent.click(within(movies).getByRole("button", { name: "Add a rule" }));
    const cards = await screen.findByRole("dialog");
    expect(within(cards).getByRole("button", { name: /^Anime/ })).toHaveTextContent(
      "Already added",
    );
    fireEvent.click(within(cards).getByRole("button", { name: /^Anime/ }));
    const dialog = await screen.findByRole("dialog");
    fireEvent.click(within(dialog).getByRole("button", { name: "Change" }));
    await choose(dialog, "Anime", "isn't anime");
    await choose(dialog, "HD version Send to", "Radarr Anime");
    fireEvent.click(within(dialog).getByRole("button", { name: "Add rule" }));
    await waitFor(() => expect(calls("POST /api/v2/admin/request-routes")).toHaveLength(1));
    expect(calls("POST /api/v2/admin/request-routes")[0]!.body).toMatchObject({
      conditions: { anime: false },
    });
  });

  it("builds a custom rule with “is none of”, named from its conditions", async () => {
    serve({ routes: ready, handlers: { "POST /api/v2/admin/request-routes": created } });
    mount();
    const movies = await section();
    fireEvent.click(within(movies).getByRole("button", { name: "Add a rule" }));
    fireEvent.click(
      within(await screen.findByRole("dialog")).getByRole("button", { name: /^Custom rule/ }),
    );
    const dialog = await screen.findByRole("dialog");
    await user().click(within(dialog).getByRole("button", { name: "Add condition" }));
    await user().click(await screen.findByRole("menuitem", { name: "Genre" }));
    await choose(dialog, "Genre match", "is none of");
    await choose(dialog, "Add a genre", "Horror");
    await choose(dialog, "Add a genre", "Thriller");
    expect(within(dialog).getByLabelText("Name")).toHaveAttribute(
      "placeholder",
      "Not Horror or Thriller",
    );
    await choose(dialog, "HD version Send to", "Radarr Anime");
    fireEvent.click(within(dialog).getByRole("button", { name: "Add rule" }));
    await waitFor(() => expect(calls("POST /api/v2/admin/request-routes")).toHaveLength(1));
    expect(calls("POST /api/v2/admin/request-routes")[0]!.body).toEqual({
      media_type: "movie",
      name: "Not Horror or Thriller",
      enabled: true,
      conditions: { exclude_genre_ids: [27, 53] },
      hd: { integration_id: "radarr-2" },
      uhd: {},
      skip_uhd: false,
    });
  });
});

describe("Where requests go: the rule editor", () => {
  const anime = route({
    id: "anime-movie",
    name: "Anime",
    conditions: { anime: true, original_languages: ["ja"], year_from: 1980, year_to: 1989 },
    hd: { integration_id: "radarr-2", overrides: { root_folder: "/anime" } },
  });

  it("shows only the conditions in use, and round-trips its edits", async () => {
    serve({
      routes: [anime, ...ready],
      handlers: {
        "PUT /api/v2/admin/request-routes/{id}": (options) => reply(options, anime, '"v2"'),
      },
    });
    mount();
    const movies = await section();
    fireEvent.click(await within(movies).findByRole("button", { name: /^Anime/ }));
    const dialog = await screen.findByRole("dialog");
    expect(
      within(dialog)
        .getAllByRole("listitem", { name: /condition$/ })
        .map((item) => item.getAttribute("aria-label")),
    ).toEqual(["Anime condition", "Original language condition", "Release year condition"]);
    // Every condition has to match; values within one are alternatives.
    expect(
      within(dialog)
        .getAllByText("and", { exact: true })
        .filter((marker) => marker.tagName === "LI"),
    ).toHaveLength(2);
    expect(
      within(dialog).getByText(
        (_, element) =>
          element?.tagName === "P" &&
          element.textContent ===
            "Takes: When a movie is anime, is originally in Japanese and came out in 1980–1989.",
      ),
    ).toBeInTheDocument();
    expect(within(dialog).getByRole("combobox", { name: "4K version Send to" })).toHaveTextContent(
      "Same as Everything else (no 4K version)",
    );
    expect(
      within(dialog).getByText(
        "Sent along with the HD version when the requester's playback limit allows 4K.",
      ),
    ).toBeTruthy();
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Remove the release year condition" }),
    );
    await choose(dialog, "4K version Send to", "Don't send a 4K version");
    fireEvent.click(within(dialog).getByRole("button", { name: "Save rule" }));

    await waitFor(() => expect(calls("PUT /api/v2/admin/request-routes/{id}")).toHaveLength(1));
    const [write] = calls("PUT /api/v2/admin/request-routes/{id}");
    expect(write).toMatchObject({
      path: { id: "anime-movie" },
      headers: { "If-Match": '"anime-movie-v1"' },
    });
    expect(write!.body).toEqual({
      name: "Anime",
      enabled: true,
      conditions: { anime: true, original_languages: ["ja"] },
      hd: { integration_id: "radarr-2", overrides: { root_folder: "/anime" } },
      uhd: {},
      skip_uhd: true,
    });
  });

  it("shows the server's field errors beside their lines", async () => {
    serve({
      routes: [anime, ...ready],
      handlers: {
        "PUT /api/v2/admin/request-routes/{id}": () =>
          Promise.reject(
            invalid([
              {
                location: "body.conditions.year_to",
                detail: "The end year comes before the start year.",
              },
              { location: "body.hd.integration_id", detail: "That server no longer exists." },
            ]),
          ),
      },
    });
    mount();
    const movies = await section();
    fireEvent.click(await within(movies).findByRole("button", { name: /^Anime/ }));
    const dialog = await screen.findByRole("dialog");
    fireEvent.click(within(dialog).getByRole("button", { name: "Save rule" }));

    const year = within(dialog).getByRole("listitem", { name: "Release year condition" });
    expect(
      await within(year).findByText("The end year comes before the start year."),
    ).toBeInTheDocument();
    expect(within(year).getByLabelText("To year")).toHaveAttribute("aria-invalid", "true");
    expect(within(dialog).getByText("That server no longer exists.")).toBeInTheDocument();
    expect(
      within(dialog).getByText("The request did not pass validation; see errors."),
    ).toBeInTheDocument();
  });

  it("offers studios by TMDB ID while requests are off", async () => {
    serve({
      requestSettings: { ...settings, requests_enabled: false },
      routes: [
        route({
          id: "ghibli",
          name: "Ghibli",
          conditions: { company_ids: [10342] },
          hd: { integration_id: "radarr-2" },
        }),
        ...ready,
      ],
    });
    mount();
    const movies = await section();
    expect(await within(movies).findByText("When a movie is made by studio 10342")).toBeTruthy();
    fireEvent.click(within(movies).getByRole("button", { name: /^Ghibli/ }));
    const dialog = await screen.findByRole("dialog");
    expect(
      within(dialog).getByText("Turn on requests to pick networks and studios by name."),
    ).toBeInTheDocument();
    expect(within(dialog).getByText("TMDB 10342")).toBeInTheDocument();
    fireEvent.change(within(dialog).getByLabelText("Studio TMDB ID"), { target: { value: "2" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Add" }));
    expect(within(dialog).getByText("TMDB 2")).toBeInTheDocument();
    expect(calls("GET /api/v2/requests/discover/studios")).toHaveLength(0);
  });
});

describe("Where requests go: warnings", () => {
  it("flags a rule that is never used and moves it above the rule that catches it", async () => {
    serve({
      routes: [
        route({
          id: "all-anime",
          name: "All anime",
          position: 0,
          conditions: { anime: true },
          hd: { integration_id: "radarr-2" },
        }),
        route({
          id: "old-anime",
          name: "Old anime",
          position: 1,
          conditions: { anime: true, year_to: 1999 },
          hd: { integration_id: "radarr-1" },
        }),
        ...ready,
      ],
      handlers: {
        "POST /api/v2/admin/request-routes/order": (options) => reply(options, { items: [] }),
      },
    });
    mount();
    const movies = await section();
    const row = (await within(movies).findByRole("switch", { name: "Old anime enabled" })).closest(
      "li",
    )!;
    expect(row).toHaveTextContent(
      "Never used: “All anime” above catches every request this rule would.",
    );
    fireEvent.click(within(row).getByRole("button", { name: "Move above" }));
    await waitFor(() => expect(calls("POST /api/v2/admin/request-routes/order")).toHaveLength(1));
    expect(calls("POST /api/v2/admin/request-routes/order")[0]!.body).toEqual({
      media_type: "movie",
      ids: ["old-anime", "all-anime"],
    });
  });

  it("flags anime below a foreign-language rule, and servers that are off, need setup or are the wrong kind", async () => {
    const off = server("sonarr-3", "Sonarr Off", "sonarr", { enabled: false });
    const bare = server("sonarr-4", "Sonarr New", "sonarr", { has_api_key: false });
    serve({
      servers: [...allServers, off, bare],
      routes: [
        ...ready,
        route({
          id: "foreign",
          name: "Foreign language",
          media_type: "series",
          position: 0,
          conditions: { exclude_original_languages: ["en"] },
          hd: { integration_id: "sonarr-3" },
        }),
        route({
          id: "anime",
          name: "Anime",
          media_type: "series",
          position: 1,
          conditions: { anime: true },
          hd: { integration_id: "sonarr-4", overrides: { series_type: "anime" } },
          uhd: { integration_id: "radarr-1" },
        }),
      ],
      handlers: {
        "POST /api/v2/admin/request-routes/order": (options) => reply(options, { items: [] }),
      },
    });
    mount();
    const series = await section("Series");
    const foreign = within(series)
      .getByRole("switch", { name: "Foreign language enabled" })
      .closest("li")!;
    expect(foreign).toHaveTextContent(
      "Sonarr Off is turned off. Requests this rule sends there can't go through until it's back on.",
    );
    const anime = within(series).getByRole("switch", { name: "Anime enabled" }).closest("li")!;
    expect(anime).toHaveTextContent(
      "Anime titles are usually Japanese, so “Foreign language” above catches most of them first.",
    );
    expect(anime).toHaveTextContent("Sonarr New needs setup.");
    expect(anime).toHaveTextContent("Radarr is a Radarr server; series need Sonarr.");
    fireEvent.click(within(anime).getByRole("button", { name: "Move above" }));
    await waitFor(() => expect(calls("POST /api/v2/admin/request-routes/order")).toHaveLength(1));
    expect(calls("POST /api/v2/admin/request-routes/order")[0]!.body).toEqual({
      media_type: "series",
      ids: ["anime", "foreign"],
    });
  });

  it("sets Sonarr's series type to Anime on an anime rule that lacks it", async () => {
    const rule = route({
      id: "anime",
      name: "Anime",
      media_type: "series",
      conditions: { anime: true },
      hd: { integration_id: "sonarr-2", overrides: { root_folder: "/anime" } },
    });
    serve({
      servers: allServers,
      routes: [...ready, rule],
      handlers: {
        "PUT /api/v2/admin/request-routes/{id}": (options) => reply(options, rule, '"v2"'),
      },
    });
    mount();
    const series = await section("Series");
    const row = within(series).getByRole("switch", { name: "Anime enabled" }).closest("li")!;
    expect(row).toHaveTextContent("Sonarr will add these as standard series.");
    fireEvent.click(within(row).getByRole("button", { name: "Set series type to Anime" }));
    await waitFor(() => expect(calls("PUT /api/v2/admin/request-routes/{id}")).toHaveLength(1));
    expect(calls("PUT /api/v2/admin/request-routes/{id}")[0]).toMatchObject({
      headers: { "If-Match": '"anime-v1"' },
      body: {
        hd: {
          integration_id: "sonarr-2",
          overrides: { root_folder: "/anime", series_type: "anime" },
        },
      },
    });
  });

  it("says Everything else makes no 4K copies while every request asks for one, and opens it", async () => {
    serve({ requestSettings: { ...settings, force_dual_quality: true }, routes: ready });
    mount();
    const movies = await section();
    const everythingElse = within(movies).getByRole("group", { name: "Everything else" });
    expect(
      await within(everythingElse).findByText(
        "“Also request a 4K version of every title” is on, but Everything else doesn't send 4K versions. Titles no rule sends to a 4K server get HD only.",
      ),
    ).toBeInTheDocument();
    fireEvent.click(within(everythingElse).getByRole("button", { name: "Choose a 4K server" }));
    const dialog = await screen.findByRole("dialog");
    expect(
      within(dialog).getByText("Sent along with the HD version for every request (General)."),
    ).toBeTruthy();
  });
});

describe("Where requests go: try a title", () => {
  const preview = {
    facts: {
      anime: false,
      genre_ids: [16],
      keyword_ids: [],
      original_language: "en",
      origin_countries: ["US"],
      year: 2019,
      network_ids: [],
      company_ids: [],
    },
    rules: [
      {
        route_id: "kids",
        route_name: "Kids & family",
        is_fallback: false,
        enabled: true,
        unmet_conditions: ["genre_ids", "max_content_rating"],
        hd: "no_match",
        uhd: "no_match",
      },
      {
        route_id: "mine",
        route_name: "For kid",
        is_fallback: false,
        enabled: true,
        unmet_conditions: [],
        hd: "sends",
        uhd: "passes",
      },
      {
        route_id: "old",
        route_name: "Old anime",
        is_fallback: false,
        enabled: false,
        unmet_conditions: [],
        hd: "no_match",
        uhd: "no_match",
      },
      {
        route_id: "fallback-series",
        route_name: "Everything else",
        is_fallback: true,
        enabled: true,
        unmet_conditions: [],
        hd: "already_decided",
        uhd: "skips",
      },
    ],
    tiers: [
      {
        quality: "1080p",
        route_id: "mine",
        route_name: "For kid",
        integration_id: "sonarr-2",
        integration_name: "Sonarr Anime",
        overrides: { root_folder: "/anime", quality_profile_id: 3 },
      },
      { quality: "2160p", route_id: "fallback-series", route_name: "Everything else" },
    ],
  };

  it("searches and explains with requests off, as a chosen account", async () => {
    serve({
      servers: allServers,
      requestSettings: { ...settings, requests_enabled: false },
      routes: [
        fallback("movie", "radarr-1"),
        route({
          id: "kids",
          name: "Kids & family",
          media_type: "series",
          position: 0,
          conditions: { genre_ids: [10751, 10762], max_content_rating: "PG" },
          hd: { integration_id: "sonarr-2" },
        }),
        route({
          id: "mine",
          name: "For kid",
          media_type: "series",
          position: 1,
          conditions: { requester_user_ids: [2] },
          hd: { integration_id: "sonarr-2" },
        }),
        route({
          id: "old",
          name: "Old anime",
          media_type: "series",
          position: 2,
          enabled: false,
          conditions: { anime: true },
          hd: { integration_id: "sonarr-2" },
        }),
        fallback("series", "sonarr-1"),
      ],
      handlers: {
        "GET /api/v2/admin/request-routes/titles": (options) =>
          reply(options, {
            items: [{ tmdb_id: 42, media_type: "series", title: "Bluey", year: 2018 }],
          }),
        "POST /api/v2/admin/request-routes/preview": (options) => reply(options, preview),
      },
    });
    mount();
    const series = await section("Series");
    expect(
      within(series).getByText(
        "See which rule a request would match and where each version would go, with the rules as saved.",
      ),
    ).toBeInTheDocument();
    await choose(series, "Requested by", "kid");
    fireEvent.change(within(series).getByRole("searchbox", { name: "Search series" }), {
      target: { value: "Blu" },
    });
    fireEvent.click(await within(series).findByRole("button", { name: /Bluey/ }));

    expect(await within(series).findByText("Bluey (2018)")).toBeInTheDocument();
    expect(calls("GET /api/v2/admin/request-routes/titles")[0]!.query).toEqual({
      media_type: "series",
      q: "Blu",
    });
    expect(calls("POST /api/v2/admin/request-routes/preview")[0]!.body).toEqual({
      media_type: "series",
      tmdb_id: 42,
      requester_user_id: "2",
    });
    expect(calls("GET /api/v2/requests/search")).toHaveLength(0);
    expect(
      within(series).getByText("English · United States · 2019 · No rating · Animation"),
    ).toBeInTheDocument();
    const result = within(series).getByText("Bluey (2018)").parentElement!;
    expect(
      await within(result).findByText(/Sonarr Anime · \/anime · Anime 1080p \(rule 2, For kid\)/),
    ).toBeInTheDocument();
    expect(result).toHaveTextContent(
      "4K version → none (Everything else doesn't send 4K versions)",
    );
    const steps = within(result)
      .getAllByRole("listitem")
      .map((item) => item.textContent)
      .slice(2);
    expect(steps).toEqual([
      "1. Kids & family — doesn't match: genre is Animation (wants Family or Kids); no rating (wants PG or lower)",
      "2. For kid — matches · decides HD",
      "Off: Old anime",
      "Everything else — decides 4K: none",
    ]);
  });
});

describe("Where requests go: names, second lines and refused conditions", () => {
  it("names networks and studios from their own lists, though their TMDB IDs overlap", async () => {
    serve({
      routes: [
        route({
          id: "wb",
          name: "Studio rule",
          conditions: { company_ids: [174] },
          hd: { integration_id: "radarr-2" },
        }),
        fallback("movie", "radarr-1"),
        route({
          id: "amc",
          name: "Network rule",
          media_type: "series",
          conditions: { network_ids: [174] },
          hd: { integration_id: "sonarr-1" },
        }),
        fallback("series", "sonarr-1"),
      ],
      handlers: {
        "GET /api/v2/requests/discover/networks": (options) =>
          reply(options, { items: [{ slug: "amc", display_name: "AMC", tmdb_id: 174 }] }),
        "GET /api/v2/requests/discover/studios": (options) =>
          reply(options, {
            items: [{ slug: "wb", display_name: "Warner Bros. Pictures", tmdb_id: 174 }],
          }),
      },
    });
    mount();
    const movies = await section();
    expect(
      await within(movies).findByText("When a movie is made by Warner Bros. Pictures"),
    ).toBeInTheDocument();
    const series = await section("Series");
    expect(await within(series).findByText("When a series is on AMC")).toBeInTheDocument();
  });

  it("adds a second line of a kind in the mode still free", async () => {
    serve({ routes: ready, handlers: { "POST /api/v2/admin/request-routes": created } });
    mount();
    const movies = await section();
    fireEvent.click(within(movies).getByRole("button", { name: "Add a rule" }));
    fireEvent.click(
      within(await screen.findByRole("dialog")).getByRole("button", { name: /^Custom rule/ }),
    );
    const dialog = await screen.findByRole("dialog");
    const addGenre = async () => {
      await user().click(within(dialog).getByRole("button", { name: "Add condition" }));
      await user().click(await screen.findByRole("menuitem", { name: "Genre" }));
    };
    await addGenre();
    await choose(dialog, "Add a genre", "Animation");
    await addGenre();
    const lines = within(dialog).getAllByRole("listitem", { name: "Genre condition" });
    expect(lines).toHaveLength(2);
    expect(within(lines[1]!).getByRole("combobox", { name: "Genre match" })).toHaveTextContent(
      "is none of",
    );
    await user().click(within(lines[1]!).getByRole("combobox", { name: "Add a genre" }));
    await user().click(await screen.findByRole("option", { name: "Family" }));
    // Both modes are in use now, so Genre has no room left.
    await user().click(within(dialog).getByRole("button", { name: "Add condition" }));
    expect(await screen.findByRole("menuitem", { name: "Genre" })).toHaveAttribute(
      "aria-disabled",
      "true",
    );
    await user().keyboard("{Escape}");
    await choose(dialog, "HD version Send to", "Radarr Anime");
    fireEvent.click(within(dialog).getByRole("button", { name: "Add rule" }));
    await waitFor(() => expect(calls("POST /api/v2/admin/request-routes")).toHaveLength(1));
    expect(calls("POST /api/v2/admin/request-routes")[0]!.body).toMatchObject({
      name: "Animation, Not Family",
      conditions: { genre_ids: [16], exclude_genre_ids: [10751] },
    });
  });

  it("keeps a preset's conditions open after the server refuses one", async () => {
    serve({
      routes: ready,
      handlers: {
        "POST /api/v2/admin/request-routes": () =>
          Promise.reject(
            invalid([
              {
                location: "body.conditions.max_content_rating",
                detail: "Choose a rating such as G, PG, PG-13 or TV-Y7.",
              },
            ]),
          ),
      },
    });
    mount();
    const movies = await section();
    fireEvent.click(within(movies).getByRole("button", { name: "Add a rule" }));
    fireEvent.click(
      within(await screen.findByRole("dialog")).getByRole("button", { name: /^Kids & family/ }),
    );
    const dialog = await screen.findByRole("dialog");
    await choose(dialog, "HD version Send to", "Radarr Anime");
    fireEvent.click(within(dialog).getByRole("button", { name: "Add rule" }));
    expect(
      await within(dialog).findByText("Choose a rating such as G, PG, PG-13 or TV-Y7."),
    ).toBeInTheDocument();
    await choose(dialog, "Highest content rating", "G");
    // The error has cleared, and the conditions are still there to edit.
    expect(within(dialog).queryByText("Choose a rating such as G, PG, PG-13 or TV-Y7.")).toBeNull();
    expect(
      within(dialog).getByRole("listitem", { name: "Content rating condition" }),
    ).toBeInTheDocument();
  });
});
