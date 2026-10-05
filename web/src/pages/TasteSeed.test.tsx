import { fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { CatalogCardItem } from "@/api/v2/catalog";

const mocks = vi.hoisted(() => ({
  fetchNextPage: vi.fn(),
  useTasteSeedItems: vi.fn(),
  submit: vi.fn(),
}));

vi.mock("@/hooks/queries/tasteSeed", () => ({
  useTasteSeedItems: () => mocks.useTasteSeedItems(),
  useSubmitTasteSeed: () => ({ isPending: false, mutateAsync: mocks.submit }),
}));

vi.mock("@/hooks/useAuth", () => ({
  useAuth: () => ({ profile: { id: "p1" } }),
}));

vi.mock("@/hooks/useDocumentTitle", () => ({
  useDocumentTitle: vi.fn(),
}));

import TasteSeed from "./TasteSeed";

interface PickerState {
  pages: { items: CatalogCardItem[] }[];
  hasNextPage: boolean;
  isFetchingNextPage?: boolean;
  isFetchNextPageError?: boolean;
}

function pickerState({
  pages,
  hasNextPage,
  isFetchingNextPage = false,
  isFetchNextPageError = false,
}: PickerState) {
  return {
    data: { pages },
    fetchNextPage: mocks.fetchNextPage,
    hasNextPage,
    isFetching: isFetchingNextPage,
    isFetchingNextPage,
    isFetchNextPageError,
    isPending: false,
  };
}

function card(id: string): CatalogCardItem {
  return { content_id: id, title: `Title ${id}`, type: "movie" } as CatalogCardItem;
}

function renderPicker() {
  return render(
    <MemoryRouter>
      <TasteSeed />
    </MemoryRouter>,
  );
}

class NoopObserver {
  observe() {}
  disconnect() {}
}

describe("TasteSeed paging", () => {
  beforeEach(() => {
    mocks.fetchNextPage.mockReset();
    mocks.useTasteSeedItems.mockReset();
    vi.stubGlobal("IntersectionObserver", NoopObserver);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  // A restricted profile's first page can hydrate to no visible card while
  // the catalog has more pages; the picker keeps loading instead of showing
  // the empty state.
  it("keeps loading past a page with no visible cards", () => {
    mocks.useTasteSeedItems.mockReturnValue(
      pickerState({ pages: [{ items: [] }], hasNextPage: true }),
    );

    renderPicker();

    expect(mocks.fetchNextPage).toHaveBeenCalledTimes(1);
    expect(screen.queryByText("No items to show yet")).toBeNull();
  });

  it("waits while the next page is loading", () => {
    mocks.useTasteSeedItems.mockReturnValue(
      pickerState({ pages: [{ items: [] }], hasNextPage: true, isFetchingNextPage: true }),
    );

    renderPicker();

    expect(mocks.fetchNextPage).not.toHaveBeenCalled();
    expect(screen.queryByText("No items to show yet")).toBeNull();
  });

  it("shows the empty state once the catalog runs out", () => {
    mocks.useTasteSeedItems.mockReturnValue(
      pickerState({ pages: [{ items: [] }, { items: [] }], hasNextPage: false }),
    );

    renderPicker();

    expect(mocks.fetchNextPage).not.toHaveBeenCalled();
    expect(screen.getByText("No items to show yet")).toBeTruthy();
  });

  it("stops retrying after a failed page", () => {
    mocks.useTasteSeedItems.mockReturnValue(
      pickerState({ pages: [{ items: [] }], hasNextPage: true, isFetchNextPageError: true }),
    );

    renderPicker();

    expect(mocks.fetchNextPage).not.toHaveBeenCalled();
    expect(screen.getByText("No items to show yet")).toBeTruthy();
  });

  it("renders the cards a later page brings", () => {
    mocks.useTasteSeedItems.mockReturnValue(
      pickerState({ pages: [{ items: [] }, { items: [card("movie-1")] }], hasNextPage: true }),
    );

    renderPicker();

    expect(mocks.fetchNextPage).not.toHaveBeenCalled();
    expect(screen.getByRole("option", { name: "Select Title movie-1" })).toBeTruthy();
  });
});

describe("TasteSeed destinations", () => {
  beforeEach(() => {
    mocks.submit.mockReset();
    mocks.submit.mockResolvedValue({ added: 3 });
    mocks.useTasteSeedItems.mockReset();
    mocks.useTasteSeedItems.mockReturnValue(
      pickerState({ pages: [{ items: [card("m1"), card("m2"), card("m3")] }], hasNextPage: false }),
    );
    vi.stubGlobal("IntersectionObserver", NoopObserver);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  function renderAt(entry: string) {
    return render(
      <MemoryRouter initialEntries={[entry]}>
        <Routes>
          <Route path="/taste-seed" element={<TasteSeed />} />
          <Route path="/" element={<p>home page</p>} />
          <Route path="/recommendations" element={<p>recommendations page</p>} />
          <Route path="/settings/playback" element={<p>settings page</p>} />
        </Routes>
      </MemoryRouter>,
    );
  }

  function pickAll() {
    for (const id of ["m1", "m2", "m3"]) {
      fireEvent.click(screen.getByRole("option", { name: `Select Title ${id}` }));
    }
  }

  // A first-run submission opens the recommendations it just shaped.
  it("opens the recommendations after a first-run submission", async () => {
    renderAt("/taste-seed");
    pickAll();
    fireEvent.click(screen.getByRole("button", { name: "Done" }));

    expect(await screen.findByText("recommendations page")).toBeTruthy();
    expect(mocks.submit).toHaveBeenCalledWith(["m1", "m2", "m3"]);
  });

  it("returns to settings after a submission from settings", async () => {
    renderAt("/taste-seed?from=settings");
    pickAll();
    fireEvent.click(screen.getByRole("button", { name: "Done" }));

    expect(await screen.findByText("settings page")).toBeTruthy();
  });

  it("goes home when the first-run picker is skipped", async () => {
    renderAt("/taste-seed");
    fireEvent.click(screen.getByRole("button", { name: "Skip" }));

    expect(await screen.findByText("home page")).toBeTruthy();
    expect(mocks.submit).not.toHaveBeenCalled();
  });
});
