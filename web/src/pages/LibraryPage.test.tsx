import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { MemoryRouter, Route, Routes } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  saveLibrarySearch: vi.fn<(libraryId: number, search: string) => Promise<void>>(),
}));

vi.mock("@/hooks/queries/libraries", () => ({
  useUserLibraries: () => ({
    data: [{ id: 7, name: "Movies", type: "movie", sort_order: 0 }],
    isLoading: false,
  }),
}));

vi.mock("@/hooks/queries/libraryPageState", () => ({
  useLibraryPageStatePreference: () => ({
    isLoading: false,
    preference: {
      version: 1,
      libraries: { "7": { search: "tab=library" } },
    },
    rememberEnabled: true,
    // A fresh wrapper models the mutation hook rerendering while the cached
    // saved value is still stale.
    saveLibrarySearch: (libraryId: number, search: string) =>
      mocks.saveLibrarySearch(libraryId, search),
  }),
}));

vi.mock("@/hooks/useDocumentTitle", () => ({
  useDocumentTitle: vi.fn(),
}));

vi.mock("@/components/LibraryHeader", () => ({
  default: () => <div>Library header</div>,
}));

vi.mock("@/components/ui/tabs", () => ({
  Tabs: ({ children }: { children: ReactNode }) => <div>{children}</div>,
  TabsContent: ({ children }: { children: ReactNode }) => <div>{children}</div>,
}));

vi.mock("./LibraryRecommended", () => ({
  default: ({ onHeroStateChange }: { onHeroStateChange: (rendered: boolean) => void }) => (
    <button type="button" onClick={() => onHeroStateChange(true)}>
      Show hero
    </button>
  ),
}));

vi.mock("./LibraryBrowse", () => ({
  default: () => <div>Library browse</div>,
}));

vi.mock("./LibraryCollections", () => ({
  default: () => <div>Library collections</div>,
}));

import LibraryPage from "./LibraryPage";

function page() {
  return (
    <MemoryRouter initialEntries={["/libraries/7?tab=library&sort=year&order=desc"]}>
      <Routes>
        <Route path="/libraries/:libraryId" element={<LibraryPage />} />
      </Routes>
    </MemoryRouter>
  );
}

function renderPage() {
  return render(page());
}

describe("LibraryPage saved state", () => {
  beforeEach(() => {
    mocks.saveLibrarySearch.mockReset();
    mocks.saveLibrarySearch.mockResolvedValue();
  });

  it("submits one save while the cached value remains stale across unrelated rerenders", async () => {
    renderPage();

    await waitFor(() => expect(mocks.saveLibrarySearch).toHaveBeenCalledTimes(1));
    const firstCall = mocks.saveLibrarySearch.mock.calls[0];
    expect(firstCall).toBeDefined();
    const [, canonicalSearch] = firstCall!;

    fireEvent.click(screen.getByRole("button", { name: "Show hero" }));
    await Promise.resolve();

    expect(mocks.saveLibrarySearch).toHaveBeenCalledTimes(1);
    expect(new URLSearchParams(canonicalSearch)).toEqual(
      new URLSearchParams("tab=library&sort=year&order=desc"),
    );
  });

  it("retries the same canonical search after a failed save", async () => {
    mocks.saveLibrarySearch.mockRejectedValueOnce(new Error("rate limited"));
    const view = renderPage();

    await waitFor(() => expect(mocks.saveLibrarySearch).toHaveBeenCalledTimes(1));
    const firstResult = mocks.saveLibrarySearch.mock.results[0]?.value;
    expect(firstResult).toBeDefined();
    await expect(firstResult).rejects.toThrow("rate limited");

    fireEvent.click(screen.getByRole("button", { name: "Show hero" }));

    await waitFor(() => expect(mocks.saveLibrarySearch).toHaveBeenCalledTimes(2));
    expect(mocks.saveLibrarySearch.mock.calls[1]).toEqual(mocks.saveLibrarySearch.mock.calls[0]);
    const retryResult = mocks.saveLibrarySearch.mock.results[1]?.value;
    expect(retryResult).toBeDefined();
    await expect(retryResult).resolves.toBeUndefined();

    view.rerender(page());
    await Promise.resolve();

    expect(mocks.saveLibrarySearch).toHaveBeenCalledTimes(2);
  });
});
