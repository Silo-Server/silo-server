import type { ReactNode } from "react";
import { MemoryRouter } from "react-router";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderToStaticMarkup } from "react-dom/server";
import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  navigate: vi.fn(),
  useQuery: vi.fn(),
  useCanRequest: vi.fn(),
  useRequestSearch: vi.fn(),
}));

vi.mock("@tanstack/react-query", async () => {
  const actual =
    await vi.importActual<typeof import("@tanstack/react-query")>("@tanstack/react-query");
  return {
    ...actual,
    useQuery: (...args: unknown[]) => mocks.useQuery(...args),
  };
});

vi.mock("@/hooks/useDebounce", () => ({
  useDebounce: <T,>(v: T) => v,
}));

vi.mock("@/hooks/useCanRequest", () => ({
  useCanRequest: () => mocks.useCanRequest(),
}));

vi.mock("@/hooks/useViewTransition", () => ({
  useViewTransitionNavigate: () => mocks.navigate,
}));

vi.mock("@/hooks/queries/useRequests", () => ({
  useRequestSearch: (...args: unknown[]) => mocks.useRequestSearch(...args),
}));

vi.mock("@/components/RequestToAddSection", () => ({
  RequestToAddSection: ({
    variant,
    query,
    libraryHadHits,
  }: {
    variant: string;
    query: string;
    libraryHadHits: boolean;
  }) => (
    <div data-testid="request-section">
      {`variant="${variant}" query="${query}" libraryHadHits="${String(libraryHadHits)}"`}
    </div>
  ),
}));

vi.mock("@/components/ui/dialog", () => ({
  Dialog: ({ children, open }: { children: ReactNode; open: boolean }) =>
    open ? <div data-testid="dialog">{children}</div> : null,
  DialogContent: ({ children }: { children: ReactNode }) => <div>{children}</div>,
  DialogTitle: ({ children }: { children: ReactNode }) => <h2>{children}</h2>,
}));

vi.mock("@/lib/thumbhash", () => ({
  decodeThumbhash: () => "",
}));

import { GlobalSearch } from "./GlobalSearch";

const browseFixture = {
  content_id: "movie-99",
  type: "movie" as const,
  title: "Test Movie",
  year: 2020,
  genres: [] as string[],
  content_rating: "PG",
  status: "matched" as const,
  rating_imdb: null as number | null,
  overview: "",
  poster_url: "",
  poster_thumbhash: "",
  backdrop_url: "",
  backdrop_thumbhash: "",
};

function renderSearchMarkup(props: Partial<Parameters<typeof GlobalSearch>[0]> = {}) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return renderToStaticMarkup(
    <QueryClientProvider client={client}>
      <MemoryRouter>
        <GlobalSearch {...props} />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe("GlobalSearch", () => {
  beforeEach(() => {
    mocks.navigate.mockReset();
    mocks.useQuery.mockReset();
    mocks.useCanRequest.mockReset();
    mocks.useRequestSearch.mockReset();
    mocks.useCanRequest.mockReturnValue({
      discoveryEnabled: false,
      isResolving: false,
      submitDisabledReason: null,
    });
    mocks.useRequestSearch.mockReturnValue({
      data: undefined,
      isLoading: false,
      isError: false,
    });
    mocks.useQuery.mockReturnValue({
      data: {
        total: 50,
        has_more: true,
        items: [browseFixture],
      },
      isFetching: false,
      isError: false,
    });
  });

  it("renders preview rows and total hint when more results exist than the preview limit", () => {
    const markup = renderSearchMarkup({ defaultOpen: true, initialQuery: "Test" });

    expect(markup).toContain('data-testid="dialog"');
    expect(markup).toContain("Test Movie");
    expect(markup).toContain("Showing top 8 of 50");
    expect(markup).toContain("Press Enter for all results");
  });

  it("disables the preview query when the dialog is closed", () => {
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    renderToStaticMarkup(
      <QueryClientProvider client={client}>
        <MemoryRouter>
          <GlobalSearch />
        </MemoryRouter>
      </QueryClientProvider>,
    );

    expect(mocks.useQuery).toHaveBeenCalled();
    const lastCall = mocks.useQuery.mock.calls[mocks.useQuery.mock.calls.length - 1]![0] as {
      enabled: boolean;
    };
    expect(lastCall.enabled).toBe(false);
  });

  it("encodes picked item IDs before navigating", async () => {
    mocks.useQuery.mockReturnValue({
      data: {
        total: 1,
        has_more: false,
        items: [
          {
            ...browseFixture,
            content_id: "ebook 1",
            type: "ebook",
            title: "A Reader",
          },
        ],
      },
      isFetching: false,
      isError: false,
    });
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    render(
      <QueryClientProvider client={client}>
        <MemoryRouter>
          <GlobalSearch defaultOpen initialQuery="Reader" />
        </MemoryRouter>
      </QueryClientProvider>,
    );

    await userEvent.click(screen.getByRole("option", { name: /A Reader/i }));

    expect(mocks.navigate).toHaveBeenCalledWith("/item/ebook%201");
  });
});

describe("GlobalSearch + RequestToAddSection wiring", () => {
  beforeEach(() => {
    mocks.navigate.mockReset();
    mocks.useQuery.mockReset();
    mocks.useCanRequest.mockReset();
    mocks.useRequestSearch.mockReset();
    mocks.useCanRequest.mockReturnValue({
      discoveryEnabled: false,
      isResolving: false,
      submitDisabledReason: null,
    });
    mocks.useRequestSearch.mockReturnValue({
      data: undefined,
      isLoading: false,
      isError: false,
    });
    mocks.useQuery.mockReturnValue({
      data: { total: 50, has_more: true, items: [browseFixture] },
      isFetching: false,
      isError: false,
    });
  });

  it("renders the section with libraryHadHits=true when library returned results", () => {
    mocks.useCanRequest.mockReturnValue({
      discoveryEnabled: true,
      isResolving: false,
      submitDisabledReason: null,
    });
    mocks.useRequestSearch.mockReturnValue({
      data: {
        page: 1,
        total_pages: 1,
        total_results: 1,
        results: [
          {
            media_type: "movie",
            tmdb_id: 1,
            title: "X",
            availability: "missing",
            request: { requestable: true },
          },
        ],
      },
      isLoading: false,
      isError: false,
    });
    const markup = renderSearchMarkup({ defaultOpen: true, initialQuery: "Dune" });

    expect(markup).toContain('data-testid="request-section"');
    expect(markup).toContain("libraryHadHits=&quot;true&quot;");
    expect(markup).toContain("variant=&quot;dialog&quot;");
  });

  it("renders the section with libraryHadHits=false when library returned 0 results", () => {
    mocks.useCanRequest.mockReturnValue({
      discoveryEnabled: true,
      isResolving: false,
      submitDisabledReason: null,
    });
    mocks.useQuery.mockReturnValue({
      data: { total: 0, has_more: false, items: [] },
      isFetching: false,
      isError: false,
    });
    mocks.useRequestSearch.mockReturnValue({
      data: {
        page: 1,
        total_pages: 1,
        total_results: 1,
        results: [
          {
            media_type: "movie",
            tmdb_id: 1,
            title: "X",
            availability: "missing",
            request: { requestable: true },
          },
        ],
      },
      isLoading: false,
      isError: false,
    });
    const markup = renderSearchMarkup({ defaultOpen: true, initialQuery: "ThisDoesNotExist" });

    expect(markup).toContain("libraryHadHits=&quot;false&quot;");
  });

  it("does not call useRequestSearch with enabled=true when discoveryEnabled is false", () => {
    mocks.useCanRequest.mockReturnValue({
      discoveryEnabled: false,
      isResolving: false,
      submitDisabledReason: null,
    });
    renderSearchMarkup({ defaultOpen: true, initialQuery: "Dune" });

    const call = mocks.useRequestSearch.mock.calls[mocks.useRequestSearch.mock.calls.length - 1];
    expect(call?.[3]).toEqual({
      enabled: false,
      requireProfile: true,
      staleTime: 5 * 60 * 1000,
    });
  });

  it("does not mount RequestToAddSection when discovery is disabled", () => {
    mocks.useCanRequest.mockReturnValue({
      discoveryEnabled: false,
      isResolving: false,
      submitDisabledReason: null,
    });
    const markup = renderSearchMarkup({ defaultOpen: true, initialQuery: "Dune" });

    expect(markup).not.toContain('data-testid="request-section"');
  });

  it("suppresses 'No matches' when library is empty and TMDB is still loading", () => {
    mocks.useCanRequest.mockReturnValue({
      discoveryEnabled: true,
      isResolving: false,
      submitDisabledReason: null,
    });
    mocks.useQuery.mockReturnValue({
      data: { total: 0, has_more: false, items: [] },
      isFetching: false,
      isError: false,
    });
    mocks.useRequestSearch.mockReturnValue({
      data: undefined,
      isLoading: true,
      isError: false,
    });
    const markup = renderSearchMarkup({ defaultOpen: true, initialQuery: "Pending" });

    expect(markup).not.toContain("No matches");
  });

  it("suppresses 'No matches' when library is empty and TMDB has missing results", () => {
    mocks.useCanRequest.mockReturnValue({
      discoveryEnabled: true,
      isResolving: false,
      submitDisabledReason: null,
    });
    mocks.useQuery.mockReturnValue({
      data: { total: 0, has_more: false, items: [] },
      isFetching: false,
      isError: false,
    });
    mocks.useRequestSearch.mockReturnValue({
      data: {
        page: 1,
        total_pages: 1,
        total_results: 1,
        results: [
          {
            media_type: "movie",
            tmdb_id: 1,
            title: "X",
            availability: "missing",
            request: { requestable: true },
          },
        ],
      },
      isLoading: false,
      isError: false,
    });
    const markup = renderSearchMarkup({ defaultOpen: true, initialQuery: "FoundOnTmdb" });

    expect(markup).not.toContain("No matches");
  });

  it("still shows 'No matches' when both library and TMDB are empty", () => {
    mocks.useCanRequest.mockReturnValue({
      discoveryEnabled: true,
      isResolving: false,
      submitDisabledReason: null,
    });
    mocks.useQuery.mockReturnValue({
      data: { total: 0, has_more: false, items: [] },
      isFetching: false,
      isError: false,
    });
    mocks.useRequestSearch.mockReturnValue({
      data: { page: 1, total_pages: 1, total_results: 0, results: [] },
      isLoading: false,
      isError: false,
    });
    const markup = renderSearchMarkup({ defaultOpen: true, initialQuery: "ZzzNothing" });

    expect(markup).toContain("No matches");
  });
});
