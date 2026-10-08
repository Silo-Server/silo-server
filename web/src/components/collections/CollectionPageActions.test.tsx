import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

import getAdminCollectionCapabilitiesOk from "../../../../contracts/api/v2/fixtures/get_admin_collection_capabilities_ok.json";
import getAdminCollectionOk from "../../../../contracts/api/v2/fixtures/get_admin_collection_ok.json";
import getCollectionCapabilitiesOk from "../../../../contracts/api/v2/fixtures/get_collection_capabilities_ok.json";
import getCollectionOk from "../../../../contracts/api/v2/fixtures/get_collection_ok.json";
import listProfilesOk from "../../../../contracts/api/v2/fixtures/list_profiles_ok.json";
import { buildLibraryCollectionCatalogHref } from "@/pages/catalogSearchParams";
import { installV2Recorder, v2Recorder } from "@/test/v2Recorder";
import {
  useCollectionPageAccess,
  type CollectionPageTarget,
} from "@/hooks/useCollectionPageAccess";
import { CollectionByline, CollectionPageActions } from "./CollectionPageActions";

vi.mock("@/api/v2/request", async () => (await import("@/test/v2Recorder")).mockV2Request());

const OWNER = { ...listProfilesOk.items[0], id: "p-owner", name: "Laura", is_primary: true };
const MAYA = { ...OWNER, id: "p-maya", name: "Maya", is_primary: false };

function profilesAnswer(items: object[]) {
  return { ...listProfilesOk, items };
}

vi.mock("@/hooks/useAuth", () => ({
  useAuth: () => ({ user: { id: 1, role: "admin" }, profile: OWNER }),
  useOptionalAuth: () => ({ user: { id: 1, role: "admin" }, profile: OWNER }),
}));

const LIBRARIES = [
  { id: 1, name: "Movies" },
  { id: 2, name: "4K Movies" },
];
/** True while the profile's hidden-library preferences are still loading. */
const preferences = vi.hoisted(() => ({ loading: false }));
vi.mock("@/hooks/queries/libraries", () => ({
  useAvailableUserLibraries: () => ({ data: LIBRARIES }),
  useUserLibraries: () => ({ data: LIBRARIES, isLoading: preferences.loading }),
}));

installV2Recorder();

beforeEach(() => {
  preferences.loading = false;
  v2Recorder.answer("GET /api/v2/profiles", profilesAnswer([OWNER]));
  v2Recorder.answer("DELETE /api/v2/collections/{id}", undefined);
  v2Recorder.answer("DELETE /api/v2/admin/collections/{id}", undefined);
  v2Recorder.answer("GET /api/v2/collections/capabilities", getCollectionCapabilitiesOk);
  v2Recorder.answer("GET /api/v2/admin/collections/capabilities", getAdminCollectionCapabilitiesOk);
});

function Page({ target }: { target: CollectionPageTarget }) {
  const access = useCollectionPageAccess(target);
  return (
    <>
      <CollectionByline access={access} />
      <CollectionPageActions access={access} libraryId={target.libraryId} />
    </>
  );
}

function Location() {
  const location = useLocation();
  return <output aria-label="location">{location.pathname + location.search}</output>;
}

function renderPage(target: CollectionPageTarget) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrap = (element: ReactNode) => (
    <>
      {element}
      <Location />
    </>
  );
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={["/catalog"]}>
        <Routes>
          <Route path="/catalog" element={wrap(<Page target={target} />)} />
          <Route path="*" element={wrap(null)} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

const location = () => screen.getByRole("status", { name: "location" });

function serverCollection(overrides: Record<string, unknown> = {}) {
  v2Recorder.answer("GET /api/v2/admin/collections/{id}", {
    ...getAdminCollectionOk,
    ...overrides,
  });
}

function personalCollection(overrides: Record<string, unknown> = {}) {
  v2Recorder.answer("GET /api/v2/collections/{id}", { ...getCollectionOk, ...overrides });
}

async function openMenu() {
  await userEvent.click(await screen.findByRole("button", { name: "More actions" }));
  return screen.findByRole("menu");
}

describe("CollectionPageActions", () => {
  it("opens a server collection in each of its libraries from Open in", async () => {
    serverCollection({ library_ids: ["1", "2"] });
    renderPage({ scope: "server", id: "c1", libraryId: 1 });

    const menu = await openMenu();
    const openIn = await within(menu).findByRole("menuitem", { name: "Open in" });
    openIn.focus();
    await userEvent.keyboard("{ArrowRight}");

    expect(await screen.findByRole("menuitem", { name: "4K Movies" })).toHaveAttribute(
      "href",
      buildLibraryCollectionCatalogHref("c1", "Original", 2),
    );
    expect(screen.getByRole("menuitem", { name: "Movies" })).toHaveAttribute(
      "href",
      buildLibraryCollectionCatalogHref("c1", "Original", 1),
    );
  });

  it("offers no Open in for a collection in one library", async () => {
    serverCollection();
    renderPage({ scope: "server", id: "c1", libraryId: 1 });

    const menu = await openMenu();
    await within(menu).findByRole("menuitem", { name: "Delete…" });
    expect(within(menu).queryByRole("menuitem", { name: "Open in" })).not.toBeInTheDocument();
  });

  it("syncs a synced list from Sync now", async () => {
    personalCollection({
      collection_type: "mdblist",
      source_url: "https://mdblist.com/lists/a/b",
    });
    v2Recorder.answer("POST /api/v2/collections/{id}/sync", {
      status: "ok",
      message: "",
      items_matched: 12,
    });
    v2Recorder.answer("GET /api/v2/collections/capabilities", {
      ...getCollectionCapabilitiesOk,
      imports: true,
    });
    renderPage({ scope: "personal", id: "c1" });

    const menu = await openMenu();
    await userEvent.click(await within(menu).findByRole("menuitem", { name: "Sync now" }));

    await waitFor(() =>
      expect(v2Recorder.operations()).toContain("POST /api/v2/collections/{id}/sync"),
    );
    expect(v2Recorder.callsOf("POST /api/v2/collections/{id}/sync")).toMatchObject([
      { path: "/api/v2/collections/c1/sync" },
    ]);
  });

  it("offers no Sync now for a synced list when your storage can't import", async () => {
    personalCollection({
      collection_type: "mdblist",
      source_url: "https://mdblist.com/lists/a/b",
    });
    v2Recorder.answer("GET /api/v2/collections/capabilities", {
      ...getCollectionCapabilitiesOk,
      imports: false,
    });
    renderPage({ scope: "personal", id: "c1" });

    const menu = await openMenu();
    await within(menu).findByRole("menuitem", { name: "Delete…" });
    await waitFor(() =>
      expect(v2Recorder.operations()).toContain("GET /api/v2/collections/capabilities"),
    );
    expect(within(menu).queryByRole("menuitem", { name: "Sync now" })).not.toBeInTheDocument();
  });

  it("offers no Sync now for a synced server list when the server can't import", async () => {
    serverCollection({
      collection_type: "mdblist",
      source_url: "https://mdblist.com/lists/a/b",
    });
    v2Recorder.answer("GET /api/v2/admin/collections/capabilities", {
      ...getAdminCollectionCapabilitiesOk,
      imports: false,
    });
    renderPage({ scope: "server", id: "c1", libraryId: 1 });

    const menu = await openMenu();
    await within(menu).findByRole("menuitem", { name: "Delete…" });
    await waitFor(() =>
      expect(v2Recorder.operations()).toContain("GET /api/v2/admin/collections/capabilities"),
    );
    expect(within(menu).queryByRole("menuitem", { name: "Sync now" })).not.toBeInTheDocument();
    expect(v2Recorder.operations()).not.toContain("GET /api/v2/collections/capabilities");
  });

  it("offers no Sync now for a manual collection", async () => {
    personalCollection();
    renderPage({ scope: "personal", id: "c1" });

    const menu = await openMenu();
    await within(menu).findByRole("menuitem", { name: "Delete…" });
    expect(within(menu).queryByRole("menuitem", { name: "Sync now" })).not.toBeInTheDocument();
  });

  it("deletes your own collection after you confirm, then goes to your collections", async () => {
    personalCollection();
    renderPage({ scope: "personal", id: "c1" });

    const menu = await openMenu();
    await userEvent.click(await within(menu).findByRole("menuitem", { name: "Delete…" }));
    const dialog = await screen.findByRole("alertdialog", { name: 'Delete "Rainy days"?' });
    await userEvent.click(within(dialog).getByRole("button", { name: "Delete" }));

    await waitFor(() => expect(location()).toHaveTextContent(/^\/collections$/));
    // The version the page read, before the delete moved it on.
    expect(v2Recorder.callsOf("DELETE /api/v2/collections/{id}")).toMatchObject([
      { path: "/api/v2/collections/c1", headers: { "If-Match": '"/api/v2/collections/c1#1"' } },
    ]);
  });

  it("deletes a server collection and returns to its library's Collections tab", async () => {
    serverCollection();
    renderPage({ scope: "server", id: "c1", libraryId: 1 });

    const menu = await openMenu();
    await userEvent.click(await within(menu).findByRole("menuitem", { name: "Delete…" }));
    const dialog = await screen.findByRole("alertdialog", { name: 'Delete "Original"?' });
    expect(dialog).toHaveTextContent("Movies");
    await userEvent.click(within(dialog).getByRole("button", { name: "Delete" }));

    await waitFor(() => expect(location()).toHaveTextContent("/library/1?tab=collections"));
    expect(v2Recorder.callsOf("DELETE /api/v2/admin/collections/{id}")).toHaveLength(1);
  });

  it("sends nothing when you cancel the delete", async () => {
    personalCollection();
    renderPage({ scope: "personal", id: "c1" });

    const menu = await openMenu();
    await userEvent.click(await within(menu).findByRole("menuitem", { name: "Delete…" }));
    const dialog = await screen.findByRole("alertdialog");
    await userEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));

    await waitFor(() => expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument());
    expect(v2Recorder.writes()).toEqual([]);
  });

  it("stays on the page when the collection changed, and deletes on the next try", async () => {
    personalCollection();
    renderPage({ scope: "personal", id: "c1" });
    const menu = await openMenu();
    await userEvent.click(await within(menu).findByRole("menuitem", { name: "Delete…" }));
    v2Recorder.bump("/api/v2/collections/c1");
    await userEvent.click(
      within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Delete" }),
    );

    // The 412 refreshes the collection, so the next Delete sends its current version.
    await waitFor(() => expect(v2Recorder.callsOf("GET /api/v2/collections/{id}")).toHaveLength(2));
    expect(location()).toHaveTextContent(/^\/catalog$/);

    await userEvent.click(
      await openMenu().then((m) => within(m).findByRole("menuitem", { name: "Delete…" })),
    );
    await userEvent.click(
      within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Delete" }),
    );
    await waitFor(() => expect(location()).toHaveTextContent(/^\/collections$/));
    expect(v2Recorder.callsOf("DELETE /api/v2/collections/{id}")).toMatchObject([
      { headers: { "If-Match": '"/api/v2/collections/c1#1"' } },
      { headers: { "If-Match": '"/api/v2/collections/c1#2"' } },
    ]);
  });

  it("names the owner of a shared collection and offers only Add to my Home", async () => {
    v2Recorder.answer("GET /api/v2/profiles", profilesAnswer([OWNER, MAYA]));
    personalCollection({ creator_profile_id: "p-maya", profile_id: "p-maya", is_shared: true });
    renderPage({ scope: "personal", id: "c1" });

    const byline = await screen.findByText(
      (_, node) => node?.textContent === "by Maya · Read-only",
    );
    expect(byline).toBeVisible();
    expect(screen.queryByRole("link", { name: "Edit" })).not.toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Add to my Home" })).toHaveAttribute(
      "href",
      "/settings/home-screen?page=home&add=collection%3Auser%3Ac1",
    );

    // ⋯ holds your library pages, and nothing that changes the collection.
    const menu = await openMenu();
    const pages = within(menu).getByRole("menuitem", { name: "Add to my library page" });
    expect(within(menu).getAllByRole("menuitem")).toEqual([pages]);
    pages.focus();
    await userEvent.keyboard("{ArrowRight}");
    expect(await screen.findByRole("menuitem", { name: "4K Movies page" })).toHaveAttribute(
      "href",
      "/settings/home-screen?page=2&add=collection%3Auser%3Ac1",
    );
    await userEvent.click(screen.getByRole("menuitem", { name: "Movies page" }));
    await waitFor(() =>
      expect(location()).toHaveTextContent(
        "/settings/home-screen?page=1&add=collection%3Auser%3Ac1",
      ),
    );
    expect(v2Recorder.writes()).toEqual([]);
  });

  it("offers no library pages until it knows which libraries the profile hides", async () => {
    // Until the preferences load, the library list still holds hidden libraries.
    preferences.loading = true;
    v2Recorder.answer("GET /api/v2/profiles", profilesAnswer([OWNER, MAYA]));
    personalCollection({ creator_profile_id: "p-maya", profile_id: "p-maya", is_shared: true });
    renderPage({ scope: "personal", id: "c1" });

    expect(await screen.findByRole("link", { name: "Add to my Home" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "More actions" })).not.toBeInTheDocument();
  });

  it("offers a shared collection only the library pages it matches", async () => {
    v2Recorder.answer("GET /api/v2/profiles", profilesAnswer([OWNER, MAYA]));
    personalCollection({
      creator_profile_id: "p-maya",
      profile_id: "p-maya",
      is_shared: true,
      collection_type: "smart",
      query_definition: { library_ids: [2] },
    });
    renderPage({ scope: "personal", id: "c1" });

    const menu = await openMenu();
    expect(
      within(menu)
        .getAllByRole("menuitem")
        .map((item) => item.textContent),
    ).toEqual(["Add to my 4K Movies page"]);
  });
});
