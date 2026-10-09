import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createMemoryRouter, RouterProvider } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { setAccessToken, setProfileId } from "@/api/client";
import type { SectionOverride, SettingsSectionEntry } from "@/api/types";
import { recipeCatalogFixture } from "@/lib/homeRows/recipeCatalogFixture.test-support";
import HomeScreenSettings from "./HomeScreenSettings";

const mocks = vi.hoisted(() => ({
  request: vi.fn(),
  success: vi.fn(),
  error: vi.fn(),
  collections: [] as Array<Record<string, unknown>>,
}));
vi.mock("@/api/v2/request", async () => ({
  ...(await vi.importActual<typeof import("@/api/v2/request")>("@/api/v2/request")),
  v2: mocks.request,
}));
vi.mock("sonner", () => ({ toast: { success: mocks.success, error: mocks.error } }));
vi.mock("@/hooks/useAuth", () => ({
  useOptionalAuth: () => ({ user: { role: "user" }, profile: { id: "p1" } }),
}));
vi.mock("@/hooks/queries/libraries", () => {
  const data = [{ id: 7, name: "Movies", type: "movies" }];
  return { useUserLibraries: () => ({ data }), useAvailableUserLibraries: () => ({ data }) };
});
vi.mock("@/hooks/queries/settingValues", () => ({
  useEffectiveSettings: () => ({ data: {}, isLoading: false }),
  useSetSettingValue: () => ({ mutate: vi.fn(), isPending: false }),
  invalidateSettingValueQueries: vi.fn(),
}));
vi.mock("@/hooks/queries/useAllUserCollections", () => ({
  useAllUserCollections: () => ({
    collections: mocks.collections,
    isLoading: false,
    isError: false,
    isFetching: false,
  }),
}));
vi.mock("@/lib/recipes", async () => ({
  ...(await vi.importActual<typeof import("@/lib/recipes")>("@/lib/recipes")),
  fetchRecipeCatalog: async () => recipeCatalogFixture,
}));

type Args = {
  query?: { scope?: string; library_id?: string };
  body?: { overrides: SectionOverride[] };
};

function entry(id: string, position: number): SettingsSectionEntry {
  return {
    id,
    section_type: "recently_added",
    title: `Row ${id}`,
    default_title: `Row ${id}`,
    featured: false,
    item_limit: 20,
    hidden: false,
    is_custom: false,
    customized: false,
    position,
    config: {},
  };
}

let saved: Record<string, SectionOverride[]>;
let puts: Array<{ page: string; overrides: SectionOverride[] }>;
/** When set, a profile PUT waits for it and fails when it rejects. */
let putGate: Promise<void> | null;
/** When set, the next read of the page's rows fails. */
let failSavedRead: boolean;
let failNextRead: boolean;

function deferred() {
  let resolve!: () => void;
  let reject!: (error: Error) => void;
  const promise = new Promise<void>((done, fail) => {
    resolve = done;
    reject = fail;
  });
  return { promise, resolve, reject };
}

/** The server rows with this profile's overrides applied, as the server resolves them. */
function resolve(key: string): SettingsSectionEntry[] {
  const overrides = saved[key] ?? [];
  const server = key === "home" ? [entry("a", 0), entry("b", 1)] : [];
  const rows: SettingsSectionEntry[] = server.map((row) => {
    const o = overrides.find((override) => override.section_id === row.id);
    return { ...row, hidden: o?.hidden ?? false, customized: Boolean(o) };
  });
  for (const own of overrides.filter((o) => !o.section_id)) {
    rows.push({
      ...entry(own.id!, own.position ?? 0),
      section_type: own.section_type!,
      title: own.title!,
      default_title: "",
      hidden: Boolean(own.hidden),
      is_custom: true,
      customized: true,
      config: own.config ?? {},
    });
  }
  return rows.sort((a, b) => a.position - b.position);
}

beforeEach(() => {
  vi.clearAllMocks();
  setAccessToken("token");
  setProfileId("p1");
  mocks.collections = [
    {
      id: "mine",
      title: "Rainy days",
      source: "user",
      group: "My Collections",
      creator_profile_id: "p1",
    },
    { id: "lib-c", title: "Studio Ghibli", source: "library", group: "Movies" },
  ];
  // Row b is hidden on this profile; adding a row must keep that.
  saved = { home: [{ section_id: "b", hidden: true, position: 1 }] };
  puts = [];
  putGate = null;
  failNextRead = false;
  failSavedRead = false;
  vi.stubGlobal(
    "IntersectionObserver",
    class {
      observe() {}
      unobserve() {}
      disconnect() {}
      takeRecords = () => [];
    },
  );
  mocks.request.mockImplementation(async (operation: string, args: Args = {}) => {
    const key = args.query?.scope === "library" ? `library:${args.query.library_id}` : "home";
    switch (operation) {
      case "GET /api/v2/profile/sections/settings":
        if (failNextRead) {
          failNextRead = false;
          throw new Error("read failed");
        }
        return { items: resolve(key) };
      case "GET /api/v2/profile/sections":
        if (failSavedRead) throw new Error("read failed");
        return { items: saved[key] ?? [] };
      case "PUT /api/v2/profile/sections":
        puts.push({ page: key, overrides: args.body!.overrides });
        await putGate;
        saved[key] = args.body!.overrides;
        return { items: saved[key] };
      case "GET /api/v2/home/sections/{id}/items":
        return { items: [] };
      case "GET /api/v2/system/identity":
        return { server_id: "server-1" };
    }
    throw new Error(`Unexpected ${operation}`);
  });
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  setProfileId(null);
  setAccessToken(null);
});

function newClient() {
  return new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
}

function setup(entryPath: string, client = newClient()) {
  const router = createMemoryRouter(
    [
      { path: "/settings/home-screen", element: <HomeScreenSettings /> },
      { path: "/collections/:id/edit", element: <h1>Collection editor</h1> },
    ],
    { initialEntries: [entryPath] },
  );
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
  return router;
}

describe("?add= on Settings > Home Screen", () => {
  it("opens Add row on the profile's own collection and saves it with every existing change", async () => {
    const router = setup("/settings/home-screen?add=collection:user:mine");
    const form = await screen.findByRole("dialog", { name: "A collection" });
    expect(within(form).getByRole("radio", { name: "Rainy days" })).toBeChecked();
    expect(router.state.location.search).toBe("");

    await userEvent.click(within(form).getByRole("button", { name: "Add row" }));
    await waitFor(() => expect(puts).toHaveLength(1));
    expect(puts[0]!.overrides).toHaveLength(3);
    expect(puts[0]!.overrides).toEqual([
      expect.objectContaining({ section_id: "a", hidden: false }),
      expect.objectContaining({ section_id: "b", hidden: true }),
      expect.objectContaining({
        section_type: "collection",
        title: "Rainy days",
        config: { user_collection_id: "mine" },
        position: 2,
      }),
    ]);
  });

  it("opens Add row on a server collection the profile can see", async () => {
    setup("/settings/home-screen?add=collection:library:lib-c");
    const form = await screen.findByRole("dialog", { name: "A collection" });
    expect(within(form).getByRole("radio", { name: "Studio Ghibli" })).toBeChecked();
  });

  it.each([
    ["an unknown", "collection:user:gone"],
    // Another profile's collection it hasn't shared isn't among the options.
    ["an unshared", "collection:user:theirs"],
    ["a server collection under the wrong kind", "collection:user:lib-c"],
  ])("says %s collection can't be added and opens nothing", async (_name, value) => {
    setup(`/settings/home-screen?add=${value}`);
    await waitFor(() =>
      expect(mocks.error).toHaveBeenCalledWith("This collection can't be added here."),
    );
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(puts).toEqual([]);
  });

  it("opens nothing on a page that can't change", async () => {
    // The profile's saved changes didn't load, so the page can't change.
    failSavedRead = true;
    setup("/settings/home-screen?add=collection:user:mine");
    await waitFor(() =>
      expect(mocks.error).toHaveBeenCalledWith("This page can't change right now."),
    );
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("goes back to the collection after adding, with a toast that can move the row", async () => {
    const save = deferred();
    putGate = save.promise;
    const router = setup(
      `/settings/home-screen?add=collection:user:mine&return=${encodeURIComponent("/collections/mine/edit")}`,
    );
    const form = await screen.findByRole("dialog", { name: "A collection" });
    await userEvent.click(within(form).getByRole("button", { name: "Add row" }));

    // Nothing claims the row was added until its save lands.
    await waitFor(() => expect(puts).toHaveLength(1));
    await act(async () => {});
    expect(router.state.location.pathname).toBe("/settings/home-screen");
    expect(mocks.success).not.toHaveBeenCalled();

    save.resolve();
    expect(await screen.findByRole("heading", { name: "Collection editor" })).toBeInTheDocument();
    expect(router.state.location.pathname).toBe("/collections/mine/edit");
    expect(router.state.historyAction).toBe("REPLACE");
    expect(router.state.location.state).toEqual({
      addedRow: {
        id: puts[0]!.overrides.find((o) => o.section_type === "collection")!.id,
        surface: "profile",
        page: { kind: "home" },
        position: 3,
      },
    });
    expect(mocks.success).toHaveBeenCalledWith(
      "Added to Home as row 3 of 3",
      expect.objectContaining({ action: expect.objectContaining({ label: "Move it" }) }),
    );

    const newId = puts[0]!.overrides.find((o) => o.section_type === "collection")!.id;
    const [, options] = mocks.success.mock.calls[0]!;
    await act(async () => {
      await options.action.onClick();
    });
    expect(await screen.findByRole("dialog", { name: "Edit row" })).toBeInTheDocument();
    expect(screen.getByLabelText("Row name")).toHaveValue("Rainy days");
    expect(router.state.location.search).toBe("?page=home");
    expect(newId).toBeTruthy();
  });

  it("stays on Home rows without claiming success when the save fails", async () => {
    putGate = Promise.reject(new Error("save failed"));
    putGate.catch(() => {});
    const router = setup(
      `/settings/home-screen?add=collection:user:mine&return=${encodeURIComponent("/collections/mine/edit")}`,
    );
    const form = await screen.findByRole("dialog", { name: "A collection" });
    await userEvent.click(within(form).getByRole("button", { name: "Add row" }));

    await waitFor(() => expect(mocks.error).toHaveBeenCalled());
    await waitFor(() => expect(screen.getByRole("button", { name: "Add row" })).toBeEnabled());
    await act(async () => {});
    expect(router.state.location.pathname).toBe("/settings/home-screen");
    expect(mocks.success).not.toHaveBeenCalled();
    expect(screen.queryByText("Rainy days")).not.toBeInTheDocument();
  });

  it("goes back to the collection once a saved row shows up after a failed refetch", async () => {
    const client = newClient();
    const router = setup(
      `/settings/home-screen?add=collection:user:mine&return=${encodeURIComponent("/collections/mine/edit")}`,
      client,
    );
    const form = await screen.findByRole("dialog", { name: "A collection" });
    // The save lands, but the read after it fails.
    failNextRead = true;
    await userEvent.click(within(form).getByRole("button", { name: "Add row" }));
    await waitFor(() => expect(puts).toHaveLength(1));
    await waitFor(() => expect(failNextRead).toBe(false));
    await act(async () => {});
    expect(router.state.location.pathname).toBe("/settings/home-screen");

    // A later read shows the row: the link still goes back.
    await act(() => client.invalidateQueries());
    expect(await screen.findByRole("heading", { name: "Collection editor" })).toBeInTheDocument();
    expect(mocks.success).toHaveBeenCalledWith("Added to Home as row 3 of 3", expect.anything());
  });

  it("follows a new link while Add row is open", async () => {
    const router = setup("/settings/home-screen?add=collection:user:mine");
    await screen.findByRole("dialog", { name: "A collection" });
    await act(() => router.navigate("/settings/home-screen?edit=a"));
    const dialog = await screen.findByRole("dialog", { name: "Edit row" });
    expect(within(dialog).getByLabelText("Row name")).toHaveValue("Row a");
  });

  it("ignores a return path outside collections", async () => {
    const router = setup("/settings/home-screen?add=collection:user:mine&return=/settings");
    const form = await screen.findByRole("dialog", { name: "A collection" });
    expect(within(form).getByRole("button", { name: "All rows" })).toBeInTheDocument();
    await userEvent.click(within(form).getByRole("button", { name: "Add row" }));
    await waitFor(() => expect(puts).toHaveLength(1));
    expect(router.state.location.pathname).toBe("/settings/home-screen");
  });
});

describe("?edit= on Settings > Home Screen", () => {
  it("opens the row in Edit row", async () => {
    setup("/settings/home-screen?edit=a");
    const dialog = await screen.findByRole("dialog", { name: "Edit row" });
    expect(within(dialog).getByLabelText("Row name")).toHaveValue("Row a");
  });

  it("says a row that is gone no longer exists and opens nothing", async () => {
    setup("/settings/home-screen?edit=gone");
    await waitFor(() => expect(mocks.error).toHaveBeenCalledWith("That row no longer exists."));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });
});
