import type { ReactNode } from "react";
import { MemoryRouter } from "react-router";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

// The request suggestions load lazily. This file holds their module back until
// the test lets it load, which the main GlobalSearch tests cannot do once any
// of them has loaded it.
const mocks = vi.hoisted(() => {
  let release = () => {};
  const loaded = new Promise<void>((resolve) => {
    release = resolve;
  });
  return { loaded, release: () => release() };
});

vi.mock("@tanstack/react-query", async () => {
  const actual =
    await vi.importActual<typeof import("@tanstack/react-query")>("@tanstack/react-query");
  return {
    ...actual,
    useQuery: () => ({
      data: {
        total: 1,
        has_more: false,
        items: [
          {
            content_id: "movie-99",
            type: "movie",
            title: "Test Movie",
            year: 2020,
            genres: [],
            status: "matched",
            poster_url: "",
            poster_thumbhash: "",
          },
        ],
      },
      isFetching: false,
      isError: false,
    }),
  };
});
vi.mock("@/hooks/useDebounce", () => ({ useDebounce: <T,>(v: T) => v }));
vi.mock("@/hooks/useCanRequest", () => ({
  useCanRequest: () => ({ discoveryEnabled: true, isResolving: false, submitDisabledReason: null }),
}));
vi.mock("@/hooks/useViewTransition", () => ({ useViewTransitionNavigate: () => vi.fn() }));
vi.mock("@/hooks/queries/useRequests", () => ({
  useRequestSearch: () => ({
    data: {
      page: 1,
      total_pages: 1,
      total_results: 1,
      results: [
        {
          media_type: "series",
          tmdb_id: 7,
          title: "Requested Show",
          availability: "missing",
          request: { requestable: true },
        },
      ],
    },
    isLoading: false,
    isError: false,
  }),
}));
vi.mock("@/hooks/queries/personSearch", () => ({
  usePersonSearch: () => ({ data: [], isFetching: false }),
}));
vi.mock("@/components/ui/dialog", () => ({
  Dialog: ({ children, open }: { children: ReactNode; open: boolean }) =>
    open ? <div>{children}</div> : null,
  DialogContent: ({ children }: { children: ReactNode }) => <div>{children}</div>,
  DialogTitle: ({ children }: { children: ReactNode }) => <h2>{children}</h2>,
}));
vi.mock("@/lib/thumbhash", () => ({ decodeThumbhash: () => "" }));
vi.mock("@/components/CardPlayOverlay", () => ({ default: () => null }));
vi.mock("@/components/RequestToAddSection", async () => {
  await mocks.loaded;
  return {
    RequestToAddSection: ({
      combobox,
    }: {
      combobox?: { listboxId: string; optionId: (index: number) => string };
    }) => (
      <div role="listbox" id={combobox?.listboxId}>
        <div role="option" aria-selected={false} id={combobox?.optionId(0)}>
          Requested Show
        </div>
      </div>
    ),
  };
});

import { GlobalSearch } from "./GlobalSearch";

describe("GlobalSearch lazy request suggestions", () => {
  it("keeps request rows out of keyboard selection until they are on screen", async () => {
    render(
      <QueryClientProvider client={new QueryClient()}>
        <MemoryRouter>
          <GlobalSearch defaultOpen initialQuery="Show" />
        </MemoryRouter>
      </QueryClientProvider>,
    );
    const input = screen.getByRole("combobox", { name: "Search" });
    input.focus();

    // Still loading: the library row is the only option.
    expect(input).toHaveAttribute("aria-controls", "global-search-library-results");
    fireEvent.keyDown(input, { key: "ArrowDown" });
    fireEvent.keyDown(input, { key: "ArrowDown" });
    expect(input).toHaveAttribute("aria-activedescendant", "search-result-0");

    mocks.release();
    await screen.findByRole("option", { name: "Requested Show" });
    fireEvent.keyDown(input, { key: "ArrowDown" });
    expect(input).toHaveAttribute("aria-activedescendant", "search-result-1");
    expect(input).toHaveAttribute(
      "aria-controls",
      "global-search-library-results global-search-request-results",
    );
  });
});
