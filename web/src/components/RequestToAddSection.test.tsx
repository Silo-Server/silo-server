import { useState } from "react";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { fireEvent, render as rtlRender, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

const mocks = vi.hoisted(() => ({
  useCanRequest: vi.fn(),
  useRequestSearch: vi.fn(),
}));

vi.mock("@/hooks/useCanRequest", () => ({
  useCanRequest: () => mocks.useCanRequest(),
}));

vi.mock("@/hooks/queries/useRequests", () => ({
  useRequestSearch: (...args: unknown[]) => mocks.useRequestSearch(...args),
}));

vi.mock("@/hooks/useWatchlistTitleToggle", () => ({
  useWatchlistTitleToggle: () => ({ enabled: false, toggle: vi.fn(), isPending: () => false }),
}));

import { RequestToAddSection } from "./RequestToAddSection";
import type { RequestToAddSectionProps } from "./RequestToAddSection";
import type { RequestMediaResult } from "@/api/types";

function render(child: ReactNode) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return renderToStaticMarkup(
    <QueryClientProvider client={client}>
      <MemoryRouter>{child}</MemoryRouter>
    </QueryClientProvider>,
  );
}

function renderInRouter(child: ReactNode) {
  return rtlRender(<MemoryRouter>{child}</MemoryRouter>);
}

type GridProps = Extract<RequestToAddSectionProps, { variant: "grid" }>;

/** Holds the grid's page the way Catalog does, minus the URL. */
function PagedGrid(props: Omit<GridProps, "variant" | "page" | "onPageChange">) {
  const [page, setPage] = useState(1);
  return <RequestToAddSection variant="grid" {...props} page={page} onPageChange={setPage} />;
}

const missingResult = (overrides: Partial<RequestMediaResult> = {}): RequestMediaResult => ({
  media_type: "movie",
  tmdb_id: 1,
  title: "Dune: Prophecy",
  year: 2024,
  availability: "missing",
  request: { requestable: true },
  ...overrides,
});

const availableResult = (overrides: Partial<RequestMediaResult> = {}): RequestMediaResult => ({
  media_type: "movie",
  tmdb_id: 2,
  title: "Dune",
  year: 2021,
  availability: "available",
  request: { requestable: false },
  ...overrides,
});

function searchReturns(results: RequestMediaResult[], isError = false) {
  mocks.useRequestSearch.mockReturnValue({
    data: { page: 1, total_pages: 1, total_results: results.length, results },
    isLoading: false,
    isError,
  });
}

beforeEach(() => {
  mocks.useCanRequest.mockReset();
  mocks.useRequestSearch.mockReset();
  mocks.useCanRequest.mockReturnValue({
    discoveryEnabled: true,
    isResolving: false,
    submitDisabledReason: null,
  });
});

describe("RequestToAddSection (dialog variant)", () => {
  it("passes enabled=false to useRequestSearch when discovery is disabled so no network call fires", () => {
    mocks.useCanRequest.mockReturnValue({
      discoveryEnabled: false,
      isResolving: false,
      submitDisabledReason: null,
    });
    searchReturns([missingResult()]);

    const markup = render(<RequestToAddSection variant="dialog" query="dune" libraryHadHits />);
    expect(markup).toBe("");

    const call = mocks.useRequestSearch.mock.calls[mocks.useRequestSearch.mock.calls.length - 1];
    expect(call?.[0]).toBe("all");
    expect(call?.[1]).toBe("dune");
    expect(call?.[2]).toBe(1);
    expect(call?.[3]).toEqual({
      enabled: false,
      requireProfile: true,
      staleTime: 5 * 60 * 1000,
      gcTime: 30_000,
      retry: false,
    });
  });

  it("does not claim media is absent while the local lookup is unresolved or failed", () => {
    searchReturns([missingResult()]);
    const markup = render(
      <RequestToAddSection
        variant="dialog"
        query="breaking bad"
        libraryHadHits={false}
        libraryResultsKnown={false}
      />,
    );
    expect(markup).toContain("Discovery matches:");
    expect(markup).not.toContain("Not in your library");
  });

  it("filters out results already available in the library", () => {
    // missingResult has tmdb_id 1, availableResult has tmdb_id 2. The DialogRow
    // renders item.title only as text content (never as a `title=` attribute), so
    // a substring check on `title="Dune"` would pass even with the filter removed.
    // Check the link target instead — it's a precise, filter-driven signal.
    searchReturns([availableResult(), missingResult()]);
    const markup = render(<RequestToAddSection variant="dialog" query="dune" libraryHadHits />);
    expect(markup).toContain("/title/movie/1");
    expect(markup).not.toContain("/title/movie/2");
  });

  it("renders nothing when TMDB returned an error", () => {
    mocks.useRequestSearch.mockReturnValue({ data: undefined, isLoading: false, isError: true });
    const markup = render(<RequestToAddSection variant="dialog" query="dune" libraryHadHits />);
    expect(markup).toBe("");
  });

  it("keeps rendering cached TMDB results when a refetch errors", () => {
    searchReturns([missingResult()], true);
    const markup = render(<RequestToAddSection variant="dialog" query="dune" libraryHadHits />);
    expect(markup).toContain("Dune: Prophecy");
  });

  it("limits the dialog variant to at most 4 rows", () => {
    const many = Array.from({ length: 10 }, (_, i) =>
      missingResult({ tmdb_id: i + 100, title: `Result ${i}` }),
    );
    searchReturns(many);
    const markup = render(<RequestToAddSection variant="dialog" query="dune" libraryHadHits />);
    expect(markup).toContain("Result 0");
    expect(markup).toContain("Result 3");
    expect(markup).not.toContain("Result 4");
  });

  it("renders the disabled affordance and reason when a row is not requestable", () => {
    searchReturns([
      missingResult({
        tmdb_id: 7,
        title: "Quota Capped Movie",
        // formatRequestReason recognises "quota_exceeded" (not "quota_exhausted");
        // assert on the produced label so a regression in that mapping is caught.
        request: { requestable: false, reason: "quota_exceeded" },
      }),
    ]);

    const markup = render(<RequestToAddSection variant="dialog" query="dune" libraryHadHits />);

    expect(markup).toContain("Quota Capped Movie");
    expect(markup).not.toContain("bg-amber-400/15");
    expect(markup).toContain("Request limit reached");
    expect(markup).toContain('title="Request limit reached"');
  });

  it("prefers request status over reason when a row is already requested", () => {
    searchReturns([
      missingResult({
        tmdb_id: 8,
        title: "Already Pending Movie",
        request: { requestable: false, reason: "blocked", status: "pending" },
      }),
    ]);

    const markup = render(<RequestToAddSection variant="dialog" query="dune" libraryHadHits />);

    expect(markup).toContain("Already Pending Movie");
    expect(markup).toContain('data-request-state="pending"');
    expect(markup).toContain(">Pending<");
    expect(markup).not.toContain('title="Blocked"');
  });
});

describe("RequestToAddSection (dialog variant in a host combobox)", () => {
  beforeEach(() => {
    searchReturns([
      missingResult({ tmdb_id: 1, title: "First" }),
      missingResult({ tmdb_id: 2, title: "Second" }),
    ]);
  });

  function renderInHost(onPick = vi.fn(), selectedIndex = -1) {
    renderInRouter(
      <RequestToAddSection
        variant="dialog"
        query="dune"
        libraryHadHits
        combobox={{
          listboxId: "host-requests",
          optionId: (index) => `host-option-${index + 3}`,
          selectedIndex,
          onPick,
        }}
      />,
    );
    return onPick;
  }

  it("exposes the rows as options under the host's ids and highlight", () => {
    renderInHost(vi.fn(), 1);

    expect(screen.getByRole("listbox", { name: "Request suggestions" })).toHaveAttribute(
      "id",
      "host-requests",
    );
    const options = screen.getAllByRole("option");
    expect(options.map((option) => option.id)).toEqual(["host-option-3", "host-option-4"]);
    expect(screen.getByRole("option", { selected: true })).toHaveTextContent("Second");
  });

  it("hands a plain click to the host instead of following the link", () => {
    const onPick = renderInHost();

    fireEvent.click(screen.getByRole("option", { name: /First/ }));

    expect(onPick).toHaveBeenCalledExactlyOnceWith(expect.objectContaining({ tmdb_id: 1 }));
  });

  it("leaves modified clicks to the link so they can open a new tab", () => {
    const onPick = renderInHost();
    // jsdom cannot open the link; drop the default action after React has seen the click.
    let followedLink = false;
    document.addEventListener(
      "click",
      (event) => {
        followedLink = !event.defaultPrevented;
        event.preventDefault();
      },
      { once: true },
    );

    fireEvent.click(screen.getByRole("option", { name: /First/ }), { metaKey: true });

    expect(onPick).not.toHaveBeenCalled();
    expect(followedLink).toBe(true);
  });
});

describe("RequestToAddSection (grid variant)", () => {
  function pages(totalPages: number) {
    mocks.useRequestSearch.mockImplementation((_type: string, query: string, page: number) => ({
      data: {
        page,
        total_pages: totalPages,
        total_results: totalPages * 2,
        results: [
          missingResult({ tmdb_id: page * 10 + 1, title: `${query} ${page}a` }),
          missingResult({ tmdb_id: page * 10 + 2, title: `${query} ${page}b` }),
        ],
      },
      isLoading: false,
      isError: false,
      isPlaceholderData: false,
    }));
  }

  function lastSearchCall() {
    return mocks.useRequestSearch.mock.calls[mocks.useRequestSearch.mock.calls.length - 1]!;
  }

  it("pages through TMDB's results", () => {
    pages(3);
    renderInRouter(<PagedGrid query="bear" mediaType="series" libraryHadHits />);

    expect(lastSearchCall().slice(0, 3)).toEqual(["series", "bear", 1]);
    expect(lastSearchCall()[3]).toMatchObject({ enabled: true, keepPreviousPage: true });
    const pager = screen.getByRole("navigation", { name: "Request to add pages" });
    expect(pager).toHaveTextContent("Page 1 of 3");
    expect(within(pager).getByRole("button", { name: "Previous" })).toBeDisabled();

    fireEvent.click(within(pager).getByRole("button", { name: "Next" }));
    expect(lastSearchCall().slice(0, 3)).toEqual(["series", "bear", 2]);
    expect(screen.getAllByRole("link", { name: "bear 2a" })[0]).toBeInTheDocument();
    expect(pager).toHaveTextContent("Page 2 of 3");

    fireEvent.click(within(pager).getByRole("button", { name: "Next" }));
    expect(within(pager).getByRole("button", { name: "Next" })).toBeDisabled();
  });

  it("opens at the page its host restored and reports page changes to the host", () => {
    pages(3);
    const onPageChange = vi.fn();
    renderInRouter(
      <RequestToAddSection
        variant="grid"
        query="bear"
        libraryHadHits
        page={2}
        onPageChange={onPageChange}
      />,
    );

    expect(lastSearchCall().slice(0, 3)).toEqual(["all", "bear", 2]);
    const pager = screen.getByRole("navigation", { name: "Request to add pages" });
    expect(pager).toHaveTextContent("Page 2 of 3");

    fireEvent.click(within(pager).getByRole("button", { name: "Next" }));
    expect(onPageChange).toHaveBeenLastCalledWith(3);
  });

  it("keeps the pager when the first page holds only library titles but more pages exist", () => {
    mocks.useRequestSearch.mockImplementation((_type: string, _query: string, page: number) => ({
      data: {
        page,
        total_pages: 2,
        total_results: 3,
        results:
          page === 1
            ? [availableResult()]
            : [missingResult({ tmdb_id: 1, title: "Dune: Prophecy" })],
      },
      isLoading: false,
      isError: false,
    }));
    renderInRouter(<PagedGrid query="dune" libraryHadHits />);

    const section = screen.getByRole("region", { name: "Request to add" });
    expect(
      within(section).getByText("Every title on this page is already in your library."),
    ).toBeVisible();
    const pager = within(section).getByRole("navigation", { name: "Request to add pages" });
    expect(pager).toHaveTextContent("Page 1 of 2");

    fireEvent.click(within(pager).getByRole("button", { name: "Next" }));
    expect(screen.getAllByRole("link", { name: "Dune: Prophecy" })[0]).toBeInTheDocument();
  });

  it("still hides the section when its only page holds only library titles", () => {
    searchReturns([availableResult()]);

    expect(render(<PagedGrid query="dune" libraryHadHits />)).toBe("");
  });

  it("keeps the section when a later page fails, with Retry and a way back", () => {
    const refetch = vi.fn();
    mocks.useRequestSearch.mockImplementation((_type: string, query: string, page: number) =>
      page === 1
        ? {
            data: {
              page,
              total_pages: 4,
              total_results: 8,
              results: [missingResult({ tmdb_id: 1, title: `${query} 1a` })],
            },
            isLoading: false,
            isError: false,
            isPlaceholderData: false,
          }
        : {
            data: undefined,
            isLoading: false,
            isError: true,
            isFetching: false,
            isPlaceholderData: false,
            refetch,
          },
    );
    renderInRouter(<PagedGrid query="dune" libraryHadHits />);

    fireEvent.click(screen.getByRole("button", { name: "Next" }));

    const section = screen.getByRole("region", { name: "Request to add" });
    expect(within(section).getByRole("heading", { level: 2 })).toHaveTextContent("Request to add");
    expect(within(section).getByRole("alert")).toHaveTextContent("Couldn’t load page 2.");
    const pager = within(section).getByRole("navigation", { name: "Request to add pages" });
    expect(pager).toHaveTextContent("Page 2");
    expect(within(pager).getByRole("button", { name: "Previous" })).toBeEnabled();
    expect(within(pager).getByRole("button", { name: "Next" })).toBeDisabled();

    fireEvent.click(within(section).getByRole("button", { name: "Retry" }));
    expect(refetch).toHaveBeenCalledOnce();

    fireEvent.click(within(section).getByRole("button", { name: "Back to page 1" }));
    expect(lastSearchCall().slice(0, 3)).toEqual(["all", "dune", 1]);
    expect(screen.getAllByRole("link", { name: "dune 1a" })[0]).toBeInTheDocument();
  });

  it("stops at TMDB's 500-page cap whatever total it reports", () => {
    pages(900);
    renderInRouter(<PagedGrid query="dune" libraryHadHits />);

    expect(screen.getByRole("navigation", { name: "Request to add pages" })).toHaveTextContent(
      "Page 1 of 500",
    );
  });
});
