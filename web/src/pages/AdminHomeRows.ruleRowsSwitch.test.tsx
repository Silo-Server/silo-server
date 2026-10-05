import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { v2Problem } from "@/api/v2/problems.test-support";
import AdminHomeRows from "./AdminHomeRows";

const mocks = vi.hoisted(() => ({
  request: vi.fn(),
  error: vi.fn(),
  success: vi.fn(),
  info: vi.fn(),
}));
vi.mock("@/api/v2/request", async () => ({
  ...(await vi.importActual<typeof import("@/api/v2/request")>("@/api/v2/request")),
  v2: mocks.request,
}));
vi.mock("sonner", () => ({
  toast: { success: mocks.success, error: mocks.error, warning: vi.fn(), info: mocks.info },
}));
vi.mock("@/hooks/queries/admin/libraries", () => ({
  useAdminLibraries: () => ({ data: [{ id: 7, name: "Movies", type: "movies" }] }),
}));
vi.mock("@/hooks/queries/admin/collections", () => ({
  useAdminCollections: () => ({ data: [] }),
}));
vi.mock("@/hooks/queries/collectionSurfaceRefresh", () => ({
  invalidateAdminCollectionQueries: vi.fn(),
}));
vi.mock("@/hooks/queries/useAllUserCollections", () => ({
  useAllUserCollections: () => ({ collections: [], isLoading: false }),
}));
vi.mock("@/lib/recipes", () => ({
  fetchRecipeCatalog: async () => ({ categories: {} }),
  previewSection: async () => ({ items: [], total_count: 0 }),
}));

type Args = {
  headers?: Record<string, string>;
  body?: Record<string, unknown>;
  onResponse?: (response: Response) => void;
};

const SETTINGS = "/api/v2/admin/settings/sections";
const row = {
  id: "a",
  title: "Recently Added",
  scope: "home",
  library_id: null,
  position: 0,
  section_type: "recently_added",
  item_limit: 20,
  featured: false,
  enabled: true,
  config: {},
  created_at: "2026-09-05T00:00:00Z",
  updated_at: "2026-09-05T00:00:00Z",
};

// The server's version of this setting depends only on its value.
const tag = (value: boolean) => `"allow-${value}"`;
let allowed: boolean;
let refusal: Error | null;
let holdWrite: Promise<void> | null;
let holdRead: Promise<void> | null;
let writes: Args[];

beforeEach(() => {
  vi.clearAllMocks();
  vi.stubGlobal(
    "ResizeObserver",
    class {
      observe() {}
      unobserve() {}
      disconnect() {}
    },
  );
  allowed = false;
  refusal = null;
  holdWrite = null;
  holdRead = null;
  writes = [];
  mocks.request.mockImplementation(async (operation: string, args: Args = {}) => {
    if (operation === `GET ${SETTINGS}`) {
      // A held read answers with the value it found when it started.
      const value = allowed;
      if (holdRead) await holdRead;
      args.onResponse?.(new Response(null, { headers: { ETag: tag(value) } }));
      return { allow_profile_custom_sections: value };
    }
    if (operation === `PUT ${SETTINGS}`) {
      writes.push(args);
      if (holdWrite) await holdWrite;
      if (refusal) throw refusal;
      if (args.headers?.["If-Match"] !== tag(allowed))
        throw v2Problem(412, "precondition_failed", "The setting changed.");
      allowed = args.body?.allow_profile_custom_sections === true;
      args.onResponse?.(new Response(null, { headers: { ETag: tag(allowed) } }));
      return { allow_profile_custom_sections: allowed };
    }
    args.onResponse?.(new Response(null, { headers: { ETag: '"rev-1"' } }));
    if (operation === "GET /api/v2/admin/sections/capabilities")
      return { available: true, reset_profiles: false, preview: false };
    if (operation === "GET /api/v2/admin/sections/order")
      return { scope: "home", library_id: null, ordered_ids: ["a"] };
    if (operation === "GET /api/v2/admin/sections") return { items: [{ ...row }] };
    throw new Error(`Unexpected ${operation}`);
  });
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

async function setup() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  client.setQueryData(["profile-section-flags"], { allow_profile_custom_sections: false });
  render(
    <MemoryRouter initialEntries={["/admin/home-rows"]}>
      <QueryClientProvider client={client}>
        <AdminHomeRows />
      </QueryClientProvider>
    </MemoryRouter>,
  );
  await screen.findByRole("button", { name: "More for Recently Added" });
  return client;
}

const settingReads = () =>
  mocks.request.mock.calls.filter(([operation]) => operation === `GET ${SETTINGS}`).length;

async function openMore() {
  await userEvent.click(screen.getByRole("button", { name: "More" }));
  const item = await screen.findByRole("menuitemcheckbox", { name: "Let profiles add rule rows" });
  await waitFor(() => expect(item).not.toHaveAttribute("data-disabled"));
  return item;
}

describe("Let profiles add rule rows", () => {
  it("reads the setting when More opens and shows it as a checkable menu item", async () => {
    await setup();
    expect(settingReads()).toBe(0);
    const item = await openMore();
    expect(settingReads()).toBe(1);
    expect(item).toHaveAttribute("aria-checked", "false");
    expect(item).toHaveAccessibleDescription(
      "Profiles can build their own “Titles matching rules” and “Editor's picks” rows.",
    );
  });

  it("turns on straight away over the version it read, keeps the menu open and refreshes the profile flag", async () => {
    const client = await setup();
    const item = await openMore();
    await userEvent.click(item);
    await waitFor(() => expect(item).toHaveAttribute("aria-checked", "true"));
    expect(writes).toHaveLength(1);
    expect(writes[0]!.headers).toEqual({ "If-Match": tag(false) });
    expect(writes[0]!.body).toEqual({ allow_profile_custom_sections: true });
    expect(screen.getByRole("menu")).toBeInTheDocument();
    expect(screen.queryByRole("dialog")).toBeNull();
    await waitFor(() =>
      expect(client.getQueryState(["profile-section-flags"])?.isInvalidated).toBe(true),
    );
    expect(mocks.success).toHaveBeenCalledWith("Profiles can now add rule rows.");
  });

  it("turns on from the keyboard with the menu kept open", async () => {
    await setup();
    const more = screen.getByRole("button", { name: "More" });
    more.focus();
    await userEvent.keyboard("{Enter}");
    const item = await screen.findByRole("menuitemcheckbox", {
      name: "Let profiles add rule rows",
    });
    await waitFor(() => expect(item).not.toHaveAttribute("data-disabled"));
    await userEvent.keyboard("{End}");
    expect(item).toHaveFocus();
    await userEvent.keyboard(" ");
    await waitFor(() => expect(item).toHaveAttribute("aria-checked", "true"));
    expect(writes).toHaveLength(1);
    expect(screen.getByRole("menu")).toBeInTheDocument();
    await userEvent.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("menu")).toBeNull());
    expect(more).toHaveFocus();
  });

  it("can't be changed again while a change is saving", async () => {
    await setup();
    const item = await openMore();
    let release!: () => void;
    holdWrite = new Promise((resolve) => {
      release = resolve;
    });
    await userEvent.click(item);
    await waitFor(() => expect(item).toHaveAttribute("data-disabled"));
    await userEvent.click(item);
    expect(writes).toHaveLength(1);
    await act(async () => release());
    await waitFor(() => expect(item).not.toHaveAttribute("data-disabled"));
    expect(item).toHaveAttribute("aria-checked", "true");
  });

  it("asks before turning off, and Cancel sends nothing and returns to More", async () => {
    allowed = true;
    await setup();
    await userEvent.click(await openMore());
    const dialog = await screen.findByRole("dialog", {
      name: "Turn off rule rows for profiles?",
    });
    expect(dialog).toHaveAccessibleDescription(
      "Profiles that already have rule rows or Editor's picks rows keep them, but can't change that page until they delete them.",
    );
    expect(screen.queryByRole("menu")).toBeNull();
    await userEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(writes).toHaveLength(0);
    await waitFor(() => expect(screen.getByRole("button", { name: "More" })).toHaveFocus());
  });

  it("turns off after the confirm", async () => {
    allowed = true;
    await setup();
    await userEvent.click(await openMore());
    const dialog = await screen.findByRole("dialog", {
      name: "Turn off rule rows for profiles?",
    });
    await userEvent.click(within(dialog).getByRole("button", { name: "Turn off" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(writes).toHaveLength(1);
    expect(writes[0]!.headers).toEqual({ "If-Match": tag(true) });
    expect(writes[0]!.body).toEqual({ allow_profile_custom_sections: false });
    expect(mocks.success).toHaveBeenCalledWith("Profiles can no longer add rule rows.");
    await waitFor(() => expect(screen.getByRole("button", { name: "More" })).toHaveFocus());
    const item = await openMore();
    expect(item).toHaveAttribute("aria-checked", "false");
  });

  it("shows the current value after another admin changed it, without writing again", async () => {
    await setup();
    const item = await openMore();
    allowed = true; // Another admin turned it on after this menu read it.
    await userEvent.click(item);
    await waitFor(() => expect(mocks.info).toHaveBeenCalledWith("Changed elsewhere; it's now On."));
    expect(writes).toHaveLength(1);
    expect(item).toHaveAttribute("aria-checked", "true");
    expect(item).not.toHaveAttribute("data-disabled");
    expect(mocks.error).not.toHaveBeenCalled();
  });

  it("stays off and disabled with the server's reason when the server refuses changes", async () => {
    refusal = v2Problem(403, "permission_denied", "This action is not available in demo mode.");
    await setup();
    const item = await openMore();
    await userEvent.click(item);
    await waitFor(() => expect(item).toHaveAttribute("data-disabled"));
    expect(item).toHaveAttribute("aria-checked", "false");
    expect(item).toHaveAccessibleDescription("This action is not available in demo mode.");
    expect(mocks.error).toHaveBeenCalledWith("This action is not available in demo mode.");
  });

  it("reads again on a later open, showing a change another admin made meanwhile", async () => {
    await setup();
    expect(await openMore()).toHaveAttribute("aria-checked", "false");
    await userEvent.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("menu")).toBeNull());
    allowed = true; // Another admin turned it on while this menu was closed.
    const item = await openMore();
    await waitFor(() => expect(item).toHaveAttribute("aria-checked", "true"));
    expect(settingReads()).toBe(2);
    expect(writes).toHaveLength(0);
  });

  it("keeps a saved change when a read that started before it finishes after it", async () => {
    await setup();
    await openMore();
    await userEvent.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("menu")).toBeNull());
    let release!: () => void;
    holdRead = new Promise((resolve) => {
      release = resolve;
    });
    const item = await openMore(); // Shows the last value while it reads again.
    await waitFor(() => expect(settingReads()).toBe(2));
    await userEvent.click(item);
    await waitFor(() =>
      expect(mocks.success).toHaveBeenCalledWith("Profiles can now add rule rows."),
    );
    expect(item).toHaveAttribute("aria-checked", "true");
    await act(async () => release());
    // The older read saw Off; it must not replace the value just saved.
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
    expect(item).toHaveAttribute("aria-checked", "true");
  });

  it("draws every menu icon in the same muted color", async () => {
    await setup();
    await openMore();
    const items = [...screen.getAllByRole("menuitem"), ...screen.getAllByRole("menuitemcheckbox")];
    expect(items.length).toBeGreaterThan(1);
    for (const item of items)
      expect(item.querySelector("svg")).toHaveClass("text-muted-foreground");
  });
});
