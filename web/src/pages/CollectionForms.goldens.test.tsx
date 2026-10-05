/**
 * Goldens: the requests the collection editor page sends to create and update
 * Manual and Smart collections, admin and personal. Later editor work changes
 * a golden here only on purpose.
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { createMemoryRouter, RouterProvider } from "react-router";
import { toast } from "sonner";
import { beforeEach, describe, expect, it, vi } from "vitest";

import getCollectionOk from "../../../contracts/api/v2/fixtures/get_collection_ok.json";
import {
  adminCapabilities,
  adminCollection,
  adminCollectionList,
  adminSmartCollection,
  emptyPreview,
  personalCapabilities,
  personalSmartCollection,
} from "@/test/fixtures/collectionAnswers";
import {
  chooseArtwork,
  openArtworkMenu,
  pasteArtworkLink,
  uploadArtwork,
} from "@/test/collectionArtwork";
import { goldens } from "@/test/fixtures/collectionBodies";
import { installV2Recorder, v2Recorder, type RecordedCall } from "@/test/v2Recorder";
import CollectionEditorPage from "./CollectionEditorPage";

vi.mock("@/api/v2/request", async () => (await import("@/test/v2Recorder")).mockV2Request());
vi.mock("@/hooks/queries/profiles", () => ({
  useProfiles: () => ({ data: [{ id: "p-owner", name: "Owner" }] }),
}));
vi.mock("@/hooks/useCurrentProfile", () => ({
  useCurrentProfile: () => ({ profile: { id: "p-owner" } }),
}));
vi.mock("@/hooks/queries/admin/libraries", () => ({
  useAdminLibraries: () => ({ data: [{ id: 1, name: "Movies", type: "movies" }] }),
}));
vi.mock("@/hooks/queries/libraries", async () => ({
  ...(await vi.importActual<typeof import("@/hooks/queries/libraries")>(
    "@/hooks/queries/libraries",
  )),
  useUserLibraries: () => ({ data: [{ id: 1, name: "Movies", type: "movies" }] }),
}));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), warning: vi.fn(), error: vi.fn() } }));

installV2Recorder();

beforeEach(() => {
  vi.stubGlobal(
    "ResizeObserver",
    class {
      observe() {}
      unobserve() {}
      disconnect() {}
    },
  );
  URL.createObjectURL = () => "blob:poster";
  HTMLElement.prototype.scrollIntoView = () => {};
  HTMLElement.prototype.hasPointerCapture = () => false;
  v2Recorder.answer("GET /api/v2/admin/collections/capabilities", adminCapabilities);
  v2Recorder.answer("GET /api/v2/collections/capabilities", personalCapabilities);
  v2Recorder.answer("GET /api/v2/admin/collections/{id}", adminCollection);
  v2Recorder.answer("GET /api/v2/admin/collections", adminCollectionList(adminCollection));
  v2Recorder.answer("POST /api/v2/admin/collections/preview", emptyPreview);
  v2Recorder.answer("POST /api/v2/collections/preview", emptyPreview);
  v2Recorder.answer("GET /api/v2/collections/{id}/items/order", {
    ordered_ids: ["movie:heat-1995"],
    has_more: false,
  });
  v2Recorder.answer("GET /api/v2/admin/collections/{id}/items", {
    items: [],
    page: { has_more: false },
  });
  v2Recorder.answer("GET /api/v2/admin/collections/{id}/items/order", {
    ordered_ids: [],
    has_more: false,
  });
});

function showPage(url: string) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  // A data router: the editor page guards unsaved changes with `useBlocker`.
  const router = createMemoryRouter(
    [
      { path: "/admin/collections", element: <p>Admin collections page</p> },
      {
        element: <CollectionEditorPage scope="server" />,
        children: [{ path: "/admin/collections/new" }, { path: "/admin/collections/:id/edit" }],
      },
      { path: "/collections", element: <p>Collections page</p> },
      {
        element: <CollectionEditorPage scope="personal" />,
        children: [{ path: "/collections/new" }, { path: "/collections/:id/edit" }],
      },
    ],
    { initialEntries: [url] },
  );
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
  return router;
}

async function closedTo(page: "Admin collections page" | "Collections page") {
  await screen.findByText(page);
}

/** The goldens hold writes only; what a page reads around them may change freely. */
function writes() {
  return v2Recorder.writes();
}

/** An artwork tile in the editor page's Look card, once it is open. */
function editorSlot(label: "Poster" | "Backdrop") {
  return screen.getByRole("group", { name: label });
}

const SLOT = { Poster: "poster", Backdrop: "backdrop" } as const;

function chooseEditorFile(label: "Poster" | "Backdrop", name: string) {
  return uploadArtwork(SLOT[label], new File(["image"], name, { type: "image/png" }));
}

function pasteEditorLink(label: "Poster" | "Backdrop", url: string) {
  return pasteArtworkLink(SLOT[label], url);
}

async function nameField() {
  return screen.findByRole("textbox", { name: "Name" });
}

function saveBar() {
  return screen.getByRole("region", { name: "Unsaved changes" });
}

/** Saves from the editor page; it stays open on the collection. */
async function saveOnPage(count: number) {
  fireEvent.click(within(saveBar()).getByRole("button", { name: "Save" }));
  await vi.waitFor(() => expect(writes()).toHaveLength(count));
  await vi.waitFor(() =>
    expect(screen.queryByRole("region", { name: "Unsaved changes" })).toBeNull(),
  );
}

describe("admin manual collections on the editor page", () => {
  it("creates a manual collection, then uploads its poster and backdrop in that order", async () => {
    const router = showPage("/admin/collections/new?type=manual&libraryId=1");
    fireEvent.change(await nameField(), { target: { value: "Staff picks" } });
    await chooseEditorFile("Poster", "poster.png");
    await pasteEditorLink("Backdrop", "https://images.example/backdrop.png");
    fireEvent.click(screen.getByRole("button", { name: "Create collection" }));
    await vi.waitFor(() =>
      expect(router.state.location.pathname).toBe("/admin/collections/c1/edit"),
    );
    expect(writes()).toEqual(goldens.adminManualCreate);
  });

  it("updates a manual collection with collection_type and without featured in the PATCH", async () => {
    v2Recorder.answer("GET /api/v2/admin/collections/{id}", { ...adminCollection, featured: true });
    showPage("/admin/collections/c1/edit?libraryId=1");
    fireEvent.change(await nameField(), { target: { value: "Renamed" } });
    await saveOnPage(1);
    expect(writes()).toEqual(goldens.adminManualUpdate);
    expect(writes()[0]!.body).not.toHaveProperty("featured");
  });

  it("deletes a staged poster removal only after the PATCH succeeds", async () => {
    v2Recorder.answer("GET /api/v2/admin/collections", adminCollectionList(withArtwork()));
    showPage("/admin/collections/c1/edit?libraryId=1");
    await chooseArtwork("poster", "Use the collage");
    await act(async () => {});
    expect(writes()).toEqual([]);
    await saveOnPage(2);
    expect(writes()).toEqual(goldens.adminStagedPosterRemoval);
  });

  it("replaces a removed backdrop with a file without deleting it", async () => {
    v2Recorder.answer("GET /api/v2/admin/collections", adminCollectionList(withArtwork()));
    showPage("/admin/collections/c1/edit?libraryId=1");
    await chooseArtwork("backdrop", "Remove backdrop");
    await chooseEditorFile("Backdrop", "backdrop.png");
    await saveOnPage(2);
    expect(writes()).toEqual(goldens.adminBackdropReplacement);
  });
});

describe("admin smart collections on the editor page", () => {
  it("creates a smart collection with its rules and sort, and stays on it", async () => {
    const router = showPage("/admin/collections/new?type=smart&libraryId=1");
    fireEvent.change(await nameField(), { target: { value: "New this month" } });
    fireEvent.click(screen.getByRole("button", { name: "Create collection" }));
    await vi.waitFor(() =>
      expect(router.state.location.pathname).toBe("/admin/collections/c1/edit"),
    );
    expect(writes()).toEqual(goldens.adminSmartCreate);
  });
});

function withArtwork() {
  return {
    ...adminCollection,
    poster_url: "https://images.example/poster.png",
    backdrop_url: "https://images.example/backdrop.png",
  };
}

/** `golden` with the collection renamed, as a rename-only save sends it. */
function renamed(golden: readonly RecordedCall[], name: string) {
  return golden.map((call) => {
    const body = call.body as Record<string, unknown>;
    return { ...call, body: { ...body, ...("title" in body ? { title: name } : { name }) } };
  });
}

describe("smart collections saved with only a new name", () => {
  const limits = [
    ["no limit", undefined],
    ["the server's no-limit sentinel", 10_000_000],
    ["a limit of 250", 250],
  ] as const;

  it.each(limits)("admin: keeps %s", async (label, limit) => {
    v2Recorder.answer(
      "GET /api/v2/admin/collections/{id}",
      adminSmartCollection(storedQuery(limit)),
    );
    showPage("/admin/collections/c1/edit?libraryId=1");
    fireEvent.change(await nameField(), { target: { value: "Renamed" } });
    await saveOnPage(1);
    expect(writes()).toEqual(renamed(goldens.adminSmartUnchanged[label], "Renamed"));
    expect(writes()[0]!.body).not.toHaveProperty("featured");
  });

  it.each(limits)("personal: keeps %s", async (label, limit) => {
    v2Recorder.answer("GET /api/v2/collections/{id}", personalSmartCollection(storedQuery(limit)));
    showPage("/collections/c1/edit");
    fireEvent.change(await nameField(), { target: { value: "Renamed" } });
    await saveOnPage(1);
    expect(writes()).toEqual(renamed(goldens.personalSmartUnchanged[label], "Renamed"));
  });
});

function storedQuery(limit: number | undefined) {
  return {
    library_ids: [1],
    match: "all",
    groups: [{ match: "all", rules: [{ field: "genre", op: "is", value: "Comedy" }] }],
    sort: { field: "added_at", order: "desc" },
    ...(limit === undefined ? {} : { limit }),
  };
}

describe("personal manual and smart collections", () => {
  it("creates a smart collection on the editor page, then uploads its poster", async () => {
    const router = showPage("/collections/new?type=smart");
    fireEvent.change(await nameField(), { target: { value: "Comfort" } });
    await chooseEditorFile("Poster", "poster.png");
    fireEvent.click(screen.getByRole("button", { name: "Create collection" }));
    await vi.waitFor(() => expect(router.state.location.pathname).toBe("/collections/c1/edit"));
    expect(writes()).toEqual(goldens.personalSmartCreate);
  });

  it("creates a manual collection and sends a pasted poster URL in the POST body", async () => {
    const router = showPage("/collections/new?type=manual");
    fireEvent.change(await nameField(), { target: { value: "Rainy days" } });
    await pasteEditorLink("Poster", "https://images.example/poster.png");
    fireEvent.click(screen.getByRole("button", { name: "Create collection" }));
    await vi.waitFor(() => expect(router.state.location.pathname).toBe("/collections/c1/edit"));
    expect(writes()).toEqual(goldens.personalManualCreate);
  });

  it("updates a manual collection with its description", async () => {
    showPage("/collections/c1/edit");
    fireEvent.change(await nameField(), { target: { value: "Renamed" } });
    await saveOnPage(1);
    expect(writes()).toEqual(goldens.personalManualUpdate);
  });
});

describe("personal poster removal waits for Save", () => {
  beforeEach(() => {
    v2Recorder.answer("GET /api/v2/collections", {
      items: [{ ...getCollectionOk, poster_url: "https://images.example/poster.png" }],
    });
  });

  async function removePosterOnPage() {
    await chooseArtwork("poster", "Use the collage");
    expect(within(editorSlot("Poster")).queryByRole("img")).toBeNull();
    expect(
      within(editorSlot("Poster")).getByText("The poster is removed when you save."),
    ).toBeTruthy();
  }

  it("manual: sends nothing when you leave without saving", async () => {
    showPage("/collections/c1/edit");
    await removePosterOnPage();
    await act(async () => {});
    expect(writes()).toEqual([]);
    fireEvent.click(screen.getByRole("link", { name: "Collections" }));
    fireEvent.click(await screen.findByRole("button", { name: "Discard" }));
    await closedTo("Collections page");
    expect(writes()).toEqual([]);
  });

  it("manual: deletes the poster after the PATCH on Save", async () => {
    showPage("/collections/c1/edit");
    await removePosterOnPage();
    await act(async () => {});
    expect(writes()).toEqual([]);
    await saveOnPage(2);
    expect(writes()).toEqual(goldens.personalStagedPosterRemoval);
  });

  it("manual: uploads a file chosen after the removal and sends no DELETE", async () => {
    showPage("/collections/c1/edit");
    await removePosterOnPage();
    await chooseEditorFile("Poster", "poster.png");
    await saveOnPage(2);
    expect(writes()).toEqual(goldens.personalPosterReplacement);
  });

  describe("smart", () => {
    beforeEach(() => {
      const smart = personalSmartCollection(storedQuery(undefined));
      v2Recorder.answer("GET /api/v2/collections/{id}", smart);
      v2Recorder.answer("GET /api/v2/collections", {
        items: [{ ...smart, poster_url: "https://images.example/poster.png" }],
      });
    });

    it("sends nothing when you leave without saving", async () => {
      showPage("/collections/c1/edit");
      await removePosterOnPage();
      await act(async () => {});
      expect(writes()).toEqual([]);
      fireEvent.click(screen.getByRole("link", { name: "Collections" }));
      fireEvent.click(await screen.findByRole("button", { name: "Discard" }));
      await closedTo("Collections page");
      expect(writes()).toEqual([]);
    });

    it("deletes the poster after the PATCH on Save", async () => {
      showPage("/collections/c1/edit");
      await removePosterOnPage();
      await saveOnPage(2);
      expect(writes()).toEqual(goldens.personalSmartStagedPosterRemoval);
    });
  });
});

describe("personal manual page: a title added before Save", () => {
  it("saves the rename with the ETag the add left behind", async () => {
    showPage("/collections/c1/edit");
    fireEvent.change(await screen.findByRole("combobox", { name: "Add a title" }), {
      target: { value: "alien" },
    });
    // The first search hit is already in the collection; add the second.
    const options = await screen.findAllByRole("option");
    fireEvent.click(options.find((option) => !option.hasAttribute("aria-disabled"))!);
    await vi.waitFor(() => expect(writes()).toHaveLength(1));
    fireEvent.change(await nameField(), { target: { value: "Renamed" } });
    await saveOnPage(2);
    expect(writes()).toEqual(goldens.personalAddThenRename);
    expect(toast.error).not.toHaveBeenCalled();
  });
});

describe("personal collages need the server's capability", () => {
  it("offers to remove the poster, not a collage, when the server doesn't report collages", async () => {
    // An older server's capabilities have no poster_collages.
    v2Recorder.answer("GET /api/v2/collections/capabilities", {
      ...personalCapabilities,
      poster_collages: undefined,
    });
    v2Recorder.answer("GET /api/v2/collections", {
      items: [{ ...getCollectionOk, poster_url: "https://images.example/poster.png" }],
    });
    showPage("/collections/c1/edit");
    const menu = await openArtworkMenu("poster");
    expect(within(menu).queryByRole("menuitem", { name: "Use the collage" })).toBeNull();
    expect(within(menu).getByRole("menuitem", { name: /^Remove/ })).toBeTruthy();
  });
});
