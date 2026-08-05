import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  ownerKey: "profile-1",
  rememberEnabled: true,
  savedSearch: "tab=library" as string | undefined,
  saveLibrarySearch: vi.fn<(libraryId: number, search: string) => Promise<void>>(),
}));

vi.mock("@/hooks/queries/libraries", () => ({
  useUserLibraries: () => ({
    data: [{ id: 7, name: "Movies", type: "movie", sort_order: 0 }],
    isLoading: false,
  }),
}));

vi.mock("@/hooks/queries/libraryPageState", () => ({
  libraryPageStateWriteRetryDelay: (error: unknown, fallbackDelayMs: number) => {
    const status =
      typeof error === "object" && error !== null && "status" in error
        ? (error as { status?: unknown }).status
        : undefined;
    if (
      typeof status === "number" &&
      status >= 400 &&
      status < 500 &&
      status !== 408 &&
      status !== 425 &&
      status !== 429
    ) {
      return null;
    }
    return fallbackDelayMs;
  },
  useLibraryPageStatePreference: () => ({
    ownerKey: mocks.ownerKey,
    isLoading: false,
    preference: {
      version: 1,
      libraries: mocks.savedSearch === undefined ? {} : { "7": { search: mocks.savedSearch } },
    },
    rememberEnabled: mocks.rememberEnabled,
    saveLibrarySearch: mocks.saveLibrarySearch,
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

function LocationProbe() {
  return <output data-testid="location-search">{useLocation().search}</output>;
}

function page(initialEntry = "/libraries/7?tab=library&sort=year&order=desc") {
  return (
    <MemoryRouter initialEntries={[initialEntry]}>
      <LocationProbe />
      <Routes>
        <Route path="/libraries/:libraryId" element={<LibraryPage />} />
      </Routes>
    </MemoryRouter>
  );
}

function renderPage(initialEntry?: string) {
  return render(page(initialEntry));
}

describe("LibraryPage saved state", () => {
  beforeEach(() => {
    mocks.ownerKey = "profile-1";
    mocks.rememberEnabled = true;
    mocks.savedSearch = "tab=library";
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
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(mocks.saveLibrarySearch).toHaveBeenCalledTimes(1);
    expect(new URLSearchParams(canonicalSearch)).toEqual(
      new URLSearchParams("tab=library&sort=year&order=desc"),
    );
  });

  it("retries the same canonical search after a failed save", async () => {
    vi.useFakeTimers();
    mocks.saveLibrarySearch.mockRejectedValueOnce(new Error("rate limited"));
    try {
      const view = renderPage();

      expect(mocks.saveLibrarySearch).toHaveBeenCalledTimes(1);
      const firstResult = mocks.saveLibrarySearch.mock.results[0]?.value;
      expect(firstResult).toBeDefined();
      await expect(firstResult).rejects.toThrow("rate limited");

      await act(async () => {
        vi.advanceTimersByTime(2_000);
        await Promise.resolve();
      });

      expect(mocks.saveLibrarySearch).toHaveBeenCalledTimes(2);
      expect(mocks.saveLibrarySearch.mock.calls[1]).toEqual(mocks.saveLibrarySearch.mock.calls[0]);
      const retryResult = mocks.saveLibrarySearch.mock.results[1]?.value;
      expect(retryResult).toBeDefined();
      await expect(retryResult).resolves.toBeUndefined();

      view.rerender(page());
      await act(async () => {
        await Promise.resolve();
        await Promise.resolve();
      });

      expect(mocks.saveLibrarySearch).toHaveBeenCalledTimes(2);
    } finally {
      vi.useRealTimers();
    }
  });

  it("stops retrying after two failed automatic retries", async () => {
    vi.useFakeTimers();
    mocks.saveLibrarySearch.mockRejectedValue(new Error("rate limited"));
    try {
      renderPage();

      expect(mocks.saveLibrarySearch).toHaveBeenCalledTimes(1);
      await mocks.saveLibrarySearch.mock.results[0]?.value.catch(() => undefined);

      await act(async () => {
        vi.advanceTimersByTime(2_000);
        await Promise.resolve();
      });
      expect(mocks.saveLibrarySearch).toHaveBeenCalledTimes(2);
      await mocks.saveLibrarySearch.mock.results[1]?.value.catch(() => undefined);

      await act(async () => {
        vi.advanceTimersByTime(5_000);
        await Promise.resolve();
      });
      expect(mocks.saveLibrarySearch).toHaveBeenCalledTimes(3);
      await mocks.saveLibrarySearch.mock.results[2]?.value.catch(() => undefined);

      await act(async () => {
        vi.advanceTimersByTime(60_000);
        await Promise.resolve();
      });
      expect(mocks.saveLibrarySearch).toHaveBeenCalledTimes(3);
    } finally {
      vi.useRealTimers();
    }
  });

  it("cancels a scheduled retry when the saved value acknowledges the write", async () => {
    vi.useFakeTimers();
    mocks.saveLibrarySearch.mockRejectedValueOnce(new Error("response connection lost"));
    try {
      const view = renderPage();

      expect(mocks.saveLibrarySearch).toHaveBeenCalledTimes(1);
      await mocks.saveLibrarySearch.mock.results[0]?.value.catch(() => undefined);
      const canonicalSearch = mocks.saveLibrarySearch.mock.calls[0]?.[1];
      expect(canonicalSearch).toBeDefined();

      mocks.savedSearch = canonicalSearch;
      view.rerender(page());
      await act(async () => {
        vi.advanceTimersByTime(60_000);
        await Promise.resolve();
        await Promise.resolve();
      });

      expect(mocks.saveLibrarySearch).toHaveBeenCalledTimes(1);
    } finally {
      vi.useRealTimers();
    }
  });

  it("retries a transient rate-limit rejection without looping", async () => {
    vi.useFakeTimers();
    const rateLimited = Object.assign(new Error("rate limited"), { status: 429 });
    mocks.saveLibrarySearch.mockRejectedValueOnce(rateLimited);
    try {
      renderPage();

      expect(mocks.saveLibrarySearch).toHaveBeenCalledTimes(1);
      await mocks.saveLibrarySearch.mock.results[0]?.value.catch(() => undefined);

      await act(async () => {
        vi.advanceTimersByTime(2_000);
        await Promise.resolve();
      });
      expect(mocks.saveLibrarySearch).toHaveBeenCalledTimes(2);
      await expect(mocks.saveLibrarySearch.mock.results[1]?.value).resolves.toBeUndefined();

      await act(async () => {
        vi.advanceTimersByTime(60_000);
        await Promise.resolve();
      });
      expect(mocks.saveLibrarySearch).toHaveBeenCalledTimes(2);
    } finally {
      vi.useRealTimers();
    }
  });

  it("does not carry a terminal retry marker into a different profile", async () => {
    const rejected = Object.assign(new Error("invalid setting"), { status: 422 });
    mocks.saveLibrarySearch.mockRejectedValueOnce(rejected);
    const view = renderPage();

    expect(mocks.saveLibrarySearch).toHaveBeenCalledTimes(1);
    await mocks.saveLibrarySearch.mock.results[0]?.value.catch(() => undefined);

    mocks.ownerKey = "profile-2";
    view.rerender(page());

    await waitFor(() => expect(mocks.saveLibrarySearch).toHaveBeenCalledTimes(2));
    await expect(mocks.saveLibrarySearch.mock.results[1]?.value).resolves.toBeUndefined();
  });

  it("does not carry an in-flight submission marker into a different profile", async () => {
    let resolveFirst: (() => void) | undefined;
    mocks.saveLibrarySearch.mockImplementationOnce(
      () =>
        new Promise<void>((resolve) => {
          resolveFirst = resolve;
        }),
    );
    const view = renderPage();

    expect(mocks.saveLibrarySearch).toHaveBeenCalledTimes(1);

    mocks.ownerKey = "profile-2";
    view.rerender(page());

    await waitFor(() => expect(mocks.saveLibrarySearch).toHaveBeenCalledTimes(2));
    await expect(mocks.saveLibrarySearch.mock.results[1]?.value).resolves.toBeUndefined();
    resolveFirst?.();
    await expect(mocks.saveLibrarySearch.mock.results[0]?.value).resolves.toBeUndefined();
  });

  it("rehydrates URL state instead of copying it into a different profile", async () => {
    mocks.savedSearch = "tab=collections";
    const view = renderPage("/libraries/7");

    await waitFor(() =>
      expect(screen.getByTestId("location-search")).toHaveTextContent("?tab=collections"),
    );
    expect(mocks.saveLibrarySearch).not.toHaveBeenCalled();

    mocks.ownerKey = "profile-2";
    mocks.savedSearch = "tab=library&sort=year&order=desc";
    view.rerender(page("/libraries/7"));

    await waitFor(() =>
      expect(screen.getByTestId("location-search")).toHaveTextContent(
        "?tab=library&sort=year&order=desc",
      ),
    );
    expect(mocks.saveLibrarySearch).not.toHaveBeenCalledWith(7, "tab=collections");
  });

  it("clears inherited URL state when the new profile has no saved value", async () => {
    mocks.savedSearch = "tab=collections";
    const view = renderPage("/libraries/7");

    await waitFor(() =>
      expect(screen.getByTestId("location-search")).toHaveTextContent("?tab=collections"),
    );

    mocks.ownerKey = "profile-2";
    mocks.savedSearch = undefined;
    view.rerender(page("/libraries/7"));

    await waitFor(() => expect(screen.getByTestId("location-search")).toBeEmptyDOMElement());
    expect(mocks.saveLibrarySearch).not.toHaveBeenCalledWith(7, "tab=collections");
  });

  it("clears inherited URL state when the new profile disables remembering", async () => {
    mocks.savedSearch = "tab=collections";
    const view = renderPage("/libraries/7");

    await waitFor(() =>
      expect(screen.getByTestId("location-search")).toHaveTextContent("?tab=collections"),
    );

    mocks.ownerKey = "profile-2";
    mocks.rememberEnabled = false;
    mocks.savedSearch = "tab=library&sort=year&order=desc";
    view.rerender(page("/libraries/7"));

    await waitFor(() => expect(screen.getByTestId("location-search")).toBeEmptyDOMElement());
    expect(mocks.saveLibrarySearch).not.toHaveBeenCalledWith(7, "tab=collections");
  });
});
