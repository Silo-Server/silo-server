import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import { toast } from "sonner";
import { beforeEach, describe, expect, it, vi } from "vitest";

import createCollectionOk from "../../../contracts/api/v2/fixtures/create_collection_ok.json";
import listCollectionsContainsItemOk from "../../../contracts/api/v2/fixtures/list_collections_contains_item_ok.json";
import listCollectionsOk from "../../../contracts/api/v2/fixtures/list_collections_ok.json";
import { goldens } from "@/test/fixtures/collectionBodies";
import { installV2Recorder, v2Recorder } from "@/test/v2Recorder";
import AddToCollectionDialog from "./AddToCollectionDialog";

vi.mock("@/api/v2/request", async () => (await import("@/test/v2Recorder")).mockV2Request());
const account = vi.hoisted(() => ({ actingAdmin: false, profileId: "p-owner" as string | null }));
vi.mock("@/hooks/useIsActingAdmin", () => ({ useIsActingAdmin: () => account.actingAdmin }));
vi.mock("@/hooks/useCurrentProfile", () => ({
  useCurrentProfile: () => ({ profile: account.profileId ? { id: account.profileId } : null }),
}));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), warning: vi.fn(), error: vi.fn() } }));

installV2Recorder();

const ITEM = "movie:heat-1995";
const [rainyDays, familyNight] = listCollectionsOk.items;

beforeEach(() => {
  vi.clearAllMocks();
  account.actingAdmin = false;
  account.profileId = "p-owner";
});

function Location() {
  return <output aria-label="location">{useLocation().pathname}</output>;
}

function show() {
  const onOpenChange = vi.fn();
  render(
    <QueryClientProvider
      client={
        new QueryClient({
          defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
        })
      }
    >
      <MemoryRouter initialEntries={["/items/heat"]}>
        <Routes>
          <Route
            path="*"
            element={
              <>
                <Location />
                <AddToCollectionDialog
                  open
                  onOpenChange={onOpenChange}
                  mediaItemId={ITEM}
                  itemTitle="Heat"
                />
              </>
            }
          />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
  return onOpenChange;
}

/** Answers the list with these collections; `collections` may change between reads. */
function listing(collections: () => unknown[]) {
  v2Recorder.answer("GET /api/v2/collections", () => ({ items: collections(), groups: [] }));
}

function dialog() {
  return within(screen.getByRole("dialog"));
}

async function createAndAdd(name: string) {
  fireEvent.change(await dialog().findByRole("textbox", { name: "New manual collection" }), {
    target: { value: name },
  });
  fireEvent.click(dialog().getByRole("button", { name: "Create and add" }));
}

describe("AddToCollectionDialog", () => {
  it("ticks the collections that already hold the title", async () => {
    v2Recorder.answer("GET /api/v2/collections", listCollectionsContainsItemOk);
    show();
    const box = await dialog().findByRole("checkbox", { name: "Rainy days" });
    expect(box.getAttribute("aria-checked")).toBe("true");
    expect(dialog().getByText("In 1 collection")).toBeTruthy();
  });

  it("removes the title when its tick is cleared, and says so", async () => {
    v2Recorder.answer("GET /api/v2/collections", listCollectionsContainsItemOk);
    show();
    fireEvent.click(await dialog().findByRole("checkbox", { name: "Rainy days" }));
    await waitFor(() =>
      expect(v2Recorder.writes()).toEqual([
        {
          operation: "DELETE /api/v2/collections/{id}/items/{item_id}",
          path: `/api/v2/collections/c1/items/${ITEM}`,
          headers: {},
        },
      ]),
    );
    expect(await dialog().findByText("Removed")).toBeTruthy();
    expect(
      dialog().getByRole("checkbox", { name: "Rainy days" }).getAttribute("aria-checked"),
    ).toBe("false");
  });

  it("flashes Added when a tick saves", async () => {
    show();
    fireEvent.click(await dialog().findByRole("checkbox", { name: "Rainy days" }));
    expect(await dialog().findByText("Added")).toBeTruthy();
    expect(dialog().getByText("In 1 collection")).toBeTruthy();
  });

  it("drops this dialog's ticks when it moves to another title", async () => {
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
    });
    const at = (item: string) => (
      <QueryClientProvider client={client}>
        <MemoryRouter>
          <AddToCollectionDialog open onOpenChange={vi.fn()} mediaItemId={item} itemTitle="Heat" />
        </MemoryRouter>
      </QueryClientProvider>
    );
    const { rerender } = render(at(ITEM));
    fireEvent.click(await dialog().findByRole("checkbox", { name: "Rainy days" }));
    expect(await dialog().findByText("In 1 collection")).toBeTruthy();

    rerender(at("movie:ronin-1998"));
    expect(await dialog().findByText("Not in a collection yet")).toBeTruthy();
    expect(
      dialog().getByRole("checkbox", { name: "Rainy days" }).getAttribute("aria-checked"),
    ).toBe("false");
  });

  it("puts the tick back and reports it when the add fails", async () => {
    v2Recorder.answer("PUT /api/v2/collections/{id}/items/{item_id}", () => {
      throw new Error("The server is busy");
    });
    show();
    const box = await dialog().findByRole("checkbox", { name: "Rainy days" });
    fireEvent.click(box);
    await waitFor(() => expect(toast.error).toHaveBeenCalled());
    await waitFor(() => expect(box.getAttribute("aria-checked")).toBe("false"));
  });

  it("puts the tick back when the add fails after a search hid its collection", async () => {
    const add: { fail?: (error: Error) => void } = {};
    v2Recorder.answer(
      "PUT /api/v2/collections/{id}/items/{item_id}",
      () => new Promise((_, reject) => (add.fail = reject)),
    );
    listing(() => [rainyDays, { ...rainyDays, id: "c5", name: "Heist films" }]);
    show();
    fireEvent.click(await dialog().findByRole("checkbox", { name: "Rainy days" }));
    expect(dialog().getByText("In 1 collection")).toBeTruthy();
    await waitFor(() => expect(add.fail).toBeDefined());
    const search = dialog().getByRole("searchbox", { name: "Find one of your collections" });
    fireEvent.change(search, { target: { value: "heist" } });
    expect(dialog().queryByRole("checkbox", { name: "Rainy days" })).toBeNull();

    await act(async () => add.fail?.(new Error("The server is busy")));
    await waitFor(() => expect(toast.error).toHaveBeenCalled());
    await waitFor(() => expect(dialog().getByText("Not in a collection yet")).toBeTruthy());
    fireEvent.change(search, { target: { value: "" } });
    expect(
      dialog().getByRole("checkbox", { name: "Rainy days" }).getAttribute("aria-checked"),
    ).toBe("false");
  });

  it("leaves out other profiles' shared collections and collections that fill themselves", async () => {
    listing(() => [
      rainyDays,
      familyNight,
      { ...rainyDays, id: "c3", name: "Smart picks", collection_type: "smart" },
      { ...rainyDays, id: "c4", name: "Top 250", collection_type: "mdblist" },
    ]);
    show();
    await dialog().findByRole("checkbox", { name: "Rainy days" });
    expect(dialog().getAllByRole("checkbox")).toHaveLength(1);
    expect(dialog().queryByText("Family night")).toBeNull();
    expect(
      dialog().getByText("Only manual collections take titles by hand.", { exact: false }),
    ).toBeTruthy();
  });

  it("marks the profile's own shared collections Shared", async () => {
    listing(() => [{ ...rainyDays, is_shared: true }]);
    show();
    await dialog().findByRole("checkbox", { name: "Rainy days" });
    expect(dialog().getByText("Shared")).toBeTruthy();
    expect(dialog().getByText("Manual · 4 titles")).toBeTruthy();
  });

  it("finds a collection by name", async () => {
    listing(() => [rainyDays, { ...rainyDays, id: "c5", name: "Heist films" }]);
    show();
    await dialog().findByRole("checkbox", { name: "Rainy days" });
    fireEvent.change(dialog().getByRole("searchbox", { name: "Find one of your collections" }), {
      target: { value: "heist" },
    });
    expect(dialog().queryByRole("checkbox", { name: "Rainy days" })).toBeNull();
    expect(dialog().getByRole("checkbox", { name: "Heist films" })).toBeTruthy();
    // The inline create row stays while searching.
    expect(dialog().getByRole("textbox", { name: "New manual collection" })).toBeTruthy();
  });

  it("lists nothing while the acting profile is unknown", async () => {
    account.profileId = null;
    let served = false;
    listing(() => {
      served = true;
      return [rainyDays];
    });
    show();
    await waitFor(() => expect(served).toBe(true));
    // Let the list read settle, so only the unknown profile keeps the dialog waiting.
    await act(() => new Promise((resolve) => setTimeout(resolve, 20)));
    expect(dialog().getByText("Loading collections…")).toBeTruthy();
    expect(dialog().queryByText("Start your first collection")).toBeNull();
    expect(dialog().queryByRole("textbox", { name: "New manual collection" })).toBeNull();
    expect(dialog().queryByRole("checkbox")).toBeNull();
    expect(dialog().queryByText("Rainy days")).toBeNull();
  });

  it("says the list couldn't load instead of offering a first collection, and retries", async () => {
    let fail = true;
    listing(() => {
      if (fail) throw new Error("The server is busy");
      return [rainyDays];
    });
    show();
    expect(await dialog().findByText("Couldn't load your collections")).toBeTruthy();
    expect(dialog().queryByText("Start your first collection")).toBeNull();
    expect(dialog().queryByRole("textbox", { name: "New manual collection" })).toBeNull();
    expect(dialog().getByRole("button", { name: "Cancel" })).toBeTruthy();

    fail = false;
    fireEvent.click(dialog().getByRole("button", { name: "Try again" }));
    expect(await dialog().findByRole("checkbox", { name: "Rainy days" })).toBeTruthy();
  });

  it("keeps focus on a tick while it saves, and ignores a second press until it lands", async () => {
    show();
    const box = await dialog().findByRole("checkbox", { name: "Rainy days" });
    act(() => box.focus());
    fireEvent.click(box);
    expect(box.getAttribute("aria-disabled")).toBe("true");
    expect((box as HTMLButtonElement).disabled).toBe(false);
    expect(document.activeElement).toBe(box);
    fireEvent.click(box);
    expect(await dialog().findByText("Added")).toBeTruthy();
    expect(v2Recorder.writes().map((call) => call.operation)).toEqual([
      "PUT /api/v2/collections/{id}/items/{item_id}",
    ]);
    expect(box.getAttribute("aria-checked")).toBe("true");
    expect(document.activeElement).toBe(box);
  });

  it("offers an inline create when the profile has no manual collection", async () => {
    let created = false;
    listing(() => (created ? [createCollectionOk] : [familyNight]));
    v2Recorder.answer("POST /api/v2/collections", () => {
      created = true;
      return createCollectionOk;
    });
    show();
    expect(await dialog().findByText("Start your first collection")).toBeTruthy();
    expect(dialog().getByRole("link", { name: "New smart collection" }).getAttribute("href")).toBe(
      "/collections/new?type=smart",
    );
    await createAndAdd("Night in");
    await waitFor(() => expect(v2Recorder.writes()).toEqual(goldens.addToNewCollection));
    const box = await dialog().findByRole("checkbox", { name: "Rainy days" });
    expect(box.getAttribute("aria-checked")).toBe("true");
  });

  it("creates a personal collection for an acting admin too", async () => {
    account.actingAdmin = true;
    show();
    await dialog().findByRole("checkbox", { name: "Rainy days" });
    await createAndAdd("Night in");
    await waitFor(() => expect(v2Recorder.writes()).toEqual(goldens.addToNewCollection));
    expect(v2Recorder.operations().filter((op) => op.includes("/admin/"))).toEqual([]);
    expect(v2Recorder.operations()).not.toContain("GET /api/v2/library/{id}/collections");
  });

  it("keeps a new collection when the title can't be added, and retries only the add", async () => {
    let failAdd = true;
    v2Recorder.answer("PUT /api/v2/collections/{id}/items/{item_id}", () => {
      if (failAdd) throw new Error("The server is busy");
      return undefined;
    });
    listing(() => []);
    show();
    await createAndAdd("Night in");
    await waitFor(() => expect(toast.warning).toHaveBeenCalled());
    const [message, options] = vi.mocked(toast.warning).mock.calls[0]!;
    expect(message).toBe("Made Night in, but couldn't add Heat");
    expect(v2Recorder.operations((call) => call.operation.startsWith("DELETE"))).toEqual([]);

    const writesBefore = v2Recorder.writes().length;
    failAdd = false;
    const tryAgain = (options as { action: { label: string; onClick: () => void } }).action;
    expect(tryAgain.label).toBe("Try again");
    await act(async () => tryAgain.onClick());
    await waitFor(() => expect(v2Recorder.writes().length).toBe(writesBefore + 1));
    expect(v2Recorder.writes().at(-1)?.operation).toBe(
      "PUT /api/v2/collections/{id}/items/{item_id}",
    );
  });

  it("opens the new collection from the toast when the add failed", async () => {
    v2Recorder.answer("PUT /api/v2/collections/{id}/items/{item_id}", () => {
      throw new Error("The server is busy");
    });
    listing(() => []);
    show();
    await createAndAdd("Night in");
    await waitFor(() => expect(toast.warning).toHaveBeenCalled());
    const options = vi.mocked(toast.warning).mock.calls[0]![1] as {
      cancel: { label: string; onClick: () => void };
    };
    expect(options.cancel.label).toBe("Open it");
    act(() => options.cancel.onClick());
    expect(screen.getByRole("status", { name: "location", hidden: true }).textContent).toBe(
      "/collections/c1/edit",
    );
  });

  it("closes from Done", async () => {
    const onOpenChange = show();
    await dialog().findByRole("checkbox", { name: "Rainy days" });
    fireEvent.click(dialog().getByRole("button", { name: "Done" }));
    expect(onOpenChange).toHaveBeenCalledWith(false);
  });
});
