import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

import getAdminCollectionCapabilitiesOk from "../../../../contracts/api/v2/fixtures/get_admin_collection_capabilities_ok.json";
import getCollectionCapabilitiesOk from "../../../../contracts/api/v2/fixtures/get_collection_capabilities_ok.json";
import type { ScopeKind } from "@/lib/collections/scope";
import { installV2Recorder, v2Recorder } from "@/test/v2Recorder";
import { NewCollectionPicker } from "./NewCollectionPicker";

vi.mock("@/api/v2/request", async () => (await import("@/test/v2Recorder")).mockV2Request());

installV2Recorder();

const ADMIN_CAPABILITIES = "GET /api/v2/admin/collections/capabilities";
const PERSONAL_CAPABILITIES = "GET /api/v2/collections/capabilities";
const IMPORT_SOURCES = ["mdblist", "tmdb", "tmdb_list"];

beforeEach(() => {
  v2Recorder.answer(ADMIN_CAPABILITIES, getAdminCollectionCapabilitiesOk);
  v2Recorder.answer(PERSONAL_CAPABILITIES, {
    ...getCollectionCapabilitiesOk,
    imports: true,
    import_sources: IMPORT_SOURCES,
  });
});

function Opener({ scope, libraryId }: { scope: ScopeKind; libraryId?: number | null }) {
  const [open, setOpen] = useState(false);
  return (
    <>
      <button type="button" onClick={() => setOpen(true)}>
        New collection
      </button>
      {open ? (
        <NewCollectionPicker scope={scope} libraryId={libraryId} onClose={() => setOpen(false)} />
      ) : null}
    </>
  );
}

function Location() {
  const location = useLocation();
  return <p data-testid="location">{location.pathname + location.search}</p>;
}

function show(scope: ScopeKind, libraryId?: number | null) {
  const list = scope === "server" ? "/admin/collections" : "/collections";
  render(
    <QueryClientProvider
      client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}
    >
      <MemoryRouter initialEntries={[list]}>
        <Routes>
          <Route path={list} element={<Opener scope={scope} libraryId={libraryId} />} />
          <Route path="*" element={<Location />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

async function open() {
  await userEvent.click(screen.getByRole("button", { name: "New collection" }));
  return screen.findByRole("dialog", { name: "New collection" });
}

describe("NewCollectionPicker", () => {
  it("offers Manual, Smart and Synced list as links that keep the selected library", async () => {
    show("server", 7);
    const dialog = await open();
    expect(dialog).toHaveAccessibleDescription(
      "What decides what's in it? Next you'll name it, fill it and choose where it shows.",
    );
    const manual = within(dialog).getByRole("link", { name: "Manual" });
    expect(manual).toHaveAttribute("href", "/admin/collections/new?type=manual&libraryId=7");
    expect(manual).toHaveAccessibleDescription(
      "You pick the titles and put them in order. Good for staff picks, a director's best, movie night.",
    );
    expect(within(dialog).getByRole("link", { name: "Smart" })).toHaveAttribute(
      "href",
      "/admin/collections/new?type=smart&libraryId=7",
    );
    expect(await within(dialog).findByRole("link", { name: "Synced list" })).toHaveAttribute(
      "href",
      "/admin/collections/new?type=synced&libraryId=7",
    );
    expect(dialog).toHaveTextContent("Next: name it and fill it in, on its own page.");
    expect(v2Recorder.callsOf(PERSONAL_CAPABILITIES)).toEqual([]);
  });

  it("follows a card to its editor page", async () => {
    show("server");
    const dialog = await open();
    await userEvent.click(within(dialog).getByRole("link", { name: "Smart" }));
    expect(screen.getByTestId("location")).toHaveTextContent("/admin/collections/new?type=smart");
  });

  it("offers admins a starter pack, keeping the selected library", async () => {
    show("server", 7);
    const dialog = await open();
    expect(await within(dialog).findByRole("link", { name: "Add a starter pack" })).toHaveAttribute(
      "href",
      "/admin/collections?libraryId=7&view=list&dialog=starter-packs",
    );
    expect(dialog).toHaveTextContent("Want a whole set at once?");
  });

  it("shows a profile only its own create paths and no server choices", async () => {
    show("personal");
    const dialog = await open();
    expect(dialog).toHaveAccessibleDescription(
      "What decides what's in it? Only you can change it. Next you'll name it, fill it and choose who sees it.",
    );
    await within(dialog).findByRole("link", { name: "Synced list" });
    const hrefs = within(dialog)
      .getAllByRole("link")
      .map((link) => link.getAttribute("href"));
    expect(hrefs).toEqual([
      "/collections/new?type=manual",
      "/collections/new?type=smart",
      "/collections/new?type=synced",
    ]);
    expect(within(dialog).getByRole("link", { name: "Manual" })).toHaveAccessibleDescription(
      "You pick the titles and put them in order. Good for a director's best, movie night, a watch order.",
    );
    expect(dialog).not.toHaveTextContent("starter pack");
    expect(v2Recorder.callsOf(ADMIN_CAPABILITIES)).toEqual([]);
  });

  it.each(["personal", "server"] as const)(
    "disables Synced list with the reason when the %s scope has no import sources",
    async (scope) => {
      v2Recorder.answer(scope === "server" ? ADMIN_CAPABILITIES : PERSONAL_CAPABILITIES, {
        ...(scope === "server" ? getAdminCollectionCapabilitiesOk : getCollectionCapabilitiesOk),
        imports: false,
        import_sources: [],
      });
      show(scope);
      const dialog = await open();
      expect(await within(dialog).findByText("Synced lists are off on this server.")).toBeVisible();
      expect(within(dialog).getByText("Synced list")).toBeVisible();
      expect(within(dialog).queryByRole("link", { name: "Synced list" })).toBeNull();
      expect(within(dialog).getAllByRole("link", { name: /^(Manual|Smart)$/ })).toHaveLength(2);
      // Starter packs make synced lists, so they aren't offered either.
      expect(within(dialog).queryByRole("link", { name: "Add a starter pack" })).toBeNull();
    },
  );

  it("holds the Synced list card's place while capabilities load", async () => {
    v2Recorder.answer(PERSONAL_CAPABILITIES, () => new Promise(() => {}));
    show("personal");
    const dialog = await open();
    expect(within(dialog).getByRole("status", { name: "Checking Synced lists…" })).toBeVisible();
    expect(within(dialog).queryByText("Synced list")).toBeNull();
    expect(within(dialog).getByRole("link", { name: "Manual" })).toBeVisible();
  });

  it("offers Retry when capabilities fail, then shows the card", async () => {
    let fail = true;
    v2Recorder.answer(PERSONAL_CAPABILITIES, () => {
      if (fail) throw new Error("offline");
      return { ...getCollectionCapabilitiesOk, imports: true, import_sources: IMPORT_SOURCES };
    });
    show("personal");
    const dialog = await open();
    expect(
      await within(dialog).findByText("Couldn't check whether Synced lists are on."),
    ).toBeVisible();
    fail = false;
    await userEvent.click(within(dialog).getByRole("button", { name: "Retry" }));
    expect(await within(dialog).findByRole("link", { name: "Synced list" })).toBeVisible();
  });

  it("starts with no card highlighted, tabs to the cards and follows one with Enter", async () => {
    show("personal");
    const dialog = await open();
    await within(dialog).findByRole("link", { name: "Synced list" });
    expect(within(dialog).getByRole("heading", { name: "New collection" })).toHaveFocus();
    await userEvent.tab();
    expect(within(dialog).getByRole("link", { name: "Manual" })).toHaveFocus();
    await userEvent.tab();
    expect(within(dialog).getByRole("link", { name: "Smart" })).toHaveFocus();
    await userEvent.keyboard("{Enter}");
    expect(screen.getByTestId("location")).toHaveTextContent("/collections/new?type=smart");
  });

  it("closes on Escape and gives focus back to the button that opened it", async () => {
    show("server");
    await open();
    await userEvent.keyboard("{Escape}");
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(screen.getByRole("button", { name: "New collection" })).toHaveFocus();
  });
});
