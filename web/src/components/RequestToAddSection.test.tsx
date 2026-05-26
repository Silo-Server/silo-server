import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

const mocks = vi.hoisted(() => ({
  useCanRequest: vi.fn(),
  useRequestSearch: vi.fn(),
  useCreateMediaRequest: vi.fn(),
  useDebounce: vi.fn(),
}));

vi.mock("@/hooks/useCanRequest", () => ({
  useCanRequest: () => mocks.useCanRequest(),
}));

vi.mock("@/hooks/queries/useRequests", () => ({
  useRequestSearch: (...args: unknown[]) => mocks.useRequestSearch(...args),
  useCreateMediaRequest: () => mocks.useCreateMediaRequest(),
}));

vi.mock("@/hooks/useDebounce", () => ({
  useDebounce: <T,>(v: T) => mocks.useDebounce(v) ?? v,
}));

import { RequestToAddSection } from "./RequestToAddSection";
import type { RequestMediaResult } from "@/api/types";

function render(child: ReactNode) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return renderToStaticMarkup(
    <QueryClientProvider client={client}>
      <MemoryRouter>{child}</MemoryRouter>
    </QueryClientProvider>,
  );
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

describe("RequestToAddSection (dialog variant)", () => {
  beforeEach(() => {
    mocks.useCanRequest.mockReset();
    mocks.useRequestSearch.mockReset();
    mocks.useDebounce.mockReset();
    mocks.useCanRequest.mockReturnValue({
      discoveryEnabled: true,
      isResolving: false,
      submitDisabledReason: null,
    });
    mocks.useDebounce.mockImplementation((v: unknown) => v);
  });

  it("renders nothing when discovery is disabled", () => {
    mocks.useCanRequest.mockReturnValue({
      discoveryEnabled: false,
      isResolving: false,
      submitDisabledReason: null,
    });
    mocks.useRequestSearch.mockReturnValue({ data: undefined, isLoading: false, isError: false });

    const markup = render(<RequestToAddSection variant="dialog" query="dune" libraryHadHits />);
    expect(markup).toBe("");
  });

  it("passes enabled=false to useRequestSearch when discovery is disabled so no network call fires", () => {
    mocks.useCanRequest.mockReturnValue({
      discoveryEnabled: false,
      isResolving: false,
      submitDisabledReason: null,
    });
    mocks.useRequestSearch.mockReturnValue({ data: undefined, isLoading: false, isError: false });

    render(<RequestToAddSection variant="dialog" query="dune" libraryHadHits />);

    const call = mocks.useRequestSearch.mock.calls[mocks.useRequestSearch.mock.calls.length - 1];
    expect(call?.[0]).toBe("all");
    expect(call?.[1]).toBe("dune");
    expect(call?.[2]).toBe(1);
    expect(call?.[3]).toEqual({ enabled: false });
  });

  it("passes enabled=true to useRequestSearch when discovery is enabled", () => {
    mocks.useCanRequest.mockReturnValue({
      discoveryEnabled: true,
      isResolving: false,
      submitDisabledReason: null,
    });
    mocks.useRequestSearch.mockReturnValue({
      data: { page: 1, total_pages: 1, total_results: 0, results: [] },
      isLoading: false,
      isError: false,
    });

    render(<RequestToAddSection variant="dialog" query="dune" libraryHadHits />);

    const call = mocks.useRequestSearch.mock.calls[mocks.useRequestSearch.mock.calls.length - 1];
    expect(call?.[3]).toEqual({ enabled: true });
  });

  it("renders 'Request to Add' header when library had hits", () => {
    mocks.useRequestSearch.mockReturnValue({
      data: { page: 1, total_pages: 1, total_results: 1, results: [missingResult()] },
      isLoading: false,
      isError: false,
    });
    const markup = render(<RequestToAddSection variant="dialog" query="dune" libraryHadHits />);
    expect(markup).toContain("Request to Add");
    expect(markup).toContain("Dune: Prophecy");
  });

  it("renders soft framing when library had 0 hits", () => {
    mocks.useRequestSearch.mockReturnValue({
      data: { page: 1, total_pages: 1, total_results: 1, results: [missingResult()] },
      isLoading: false,
      isError: false,
    });
    const markup = render(
      <RequestToAddSection variant="dialog" query="dune" libraryHadHits={false} />,
    );
    expect(markup).toContain("Not in your library, but you can request");
    expect(markup).not.toContain("Request to Add");
  });

  it("filters out results already available in the library", () => {
    // missingResult has tmdb_id 1, availableResult has tmdb_id 2. The DialogRow
    // renders item.title only as text content (never as a `title=` attribute), so
    // a substring check on `title="Dune"` would pass even with the filter removed.
    // Check the link target instead — it's a precise, filter-driven signal.
    mocks.useRequestSearch.mockReturnValue({
      data: {
        page: 1,
        total_pages: 1,
        total_results: 2,
        results: [availableResult(), missingResult()],
      },
      isLoading: false,
      isError: false,
    });
    const markup = render(<RequestToAddSection variant="dialog" query="dune" libraryHadHits />);
    expect(markup).toContain("/requests/movie/1");
    expect(markup).not.toContain("/requests/movie/2");
  });

  it("renders nothing when TMDB returned an error", () => {
    mocks.useRequestSearch.mockReturnValue({ data: undefined, isLoading: false, isError: true });
    const markup = render(<RequestToAddSection variant="dialog" query="dune" libraryHadHits />);
    expect(markup).toBe("");
  });

  it("renders nothing when all TMDB results are already in the library", () => {
    mocks.useRequestSearch.mockReturnValue({
      data: { page: 1, total_pages: 1, total_results: 1, results: [availableResult()] },
      isLoading: false,
      isError: false,
    });
    const markup = render(<RequestToAddSection variant="dialog" query="dune" libraryHadHits />);
    expect(markup).toBe("");
  });

  it("limits the dialog variant to at most 4 rows", () => {
    const many = Array.from({ length: 10 }, (_, i) =>
      missingResult({ tmdb_id: i + 100, title: `Result ${i}` }),
    );
    mocks.useRequestSearch.mockReturnValue({
      data: { page: 1, total_pages: 1, total_results: many.length, results: many },
      isLoading: false,
      isError: false,
    });
    const markup = render(<RequestToAddSection variant="dialog" query="dune" libraryHadHits />);
    expect(markup).toContain("Result 0");
    expect(markup).toContain("Result 3");
    expect(markup).not.toContain("Result 4");
  });

  it("renders the disabled affordance and reason when a row is not requestable", () => {
    mocks.useRequestSearch.mockReturnValue({
      data: {
        page: 1,
        total_pages: 1,
        total_results: 1,
        results: [
          missingResult({
            tmdb_id: 7,
            title: "Quota Capped Movie",
            // formatRequestReason recognises "quota_exceeded" (not "quota_exhausted");
            // assert on the produced label so a regression in that mapping is caught.
            request: { requestable: false, reason: "quota_exceeded" },
          }),
        ],
      },
      isLoading: false,
      isError: false,
    });

    const markup = render(<RequestToAddSection variant="dialog" query="dune" libraryHadHits />);

    expect(markup).toContain("Quota Capped Movie");
    expect(markup).not.toContain("bg-amber-400/15");
    expect(markup).toContain("Limit reached");
    expect(markup).toContain('title="Limit reached"');
  });
});

describe("RequestToAddSection (grid variant)", () => {
  beforeEach(() => {
    mocks.useCanRequest.mockReset();
    mocks.useRequestSearch.mockReset();
    mocks.useCreateMediaRequest.mockReset();
    mocks.useCanRequest.mockReturnValue({
      discoveryEnabled: true,
      isResolving: false,
      submitDisabledReason: null,
    });
    mocks.useCreateMediaRequest.mockReturnValue({
      mutate: vi.fn(),
      isPending: false,
      variables: undefined,
    });
  });

  it("renders a card per result with the Request to Add header when library had hits", () => {
    mocks.useRequestSearch.mockReturnValue({
      data: {
        page: 1,
        total_pages: 1,
        total_results: 2,
        results: [
          missingResult({ tmdb_id: 1, title: "Dune: Prophecy" }),
          missingResult({ tmdb_id: 2, title: "Dune (1984)" }),
        ],
      },
      isLoading: false,
      isError: false,
    });
    const markup = render(<RequestToAddSection variant="grid" query="dune" libraryHadHits />);
    expect(markup).toContain("Request to Add");
    expect(markup).toContain("Dune: Prophecy");
    expect(markup).toContain("Dune (1984)");
  });

  it("renders the soft framing in the grid variant when library had 0 hits", () => {
    mocks.useRequestSearch.mockReturnValue({
      data: {
        page: 1,
        total_pages: 1,
        total_results: 1,
        results: [missingResult({ tmdb_id: 1, title: "Dune: Prophecy" })],
      },
      isLoading: false,
      isError: false,
    });
    const markup = render(
      <RequestToAddSection variant="grid" query="dune" libraryHadHits={false} />,
    );
    expect(markup).toContain("Not in your library, but you can request");
  });

  it("limits the grid to at most 20 cards", () => {
    const many = Array.from({ length: 30 }, (_, i) =>
      missingResult({ tmdb_id: i + 100, title: `Result ${i}` }),
    );
    mocks.useRequestSearch.mockReturnValue({
      data: { page: 1, total_pages: 1, total_results: many.length, results: many },
      isLoading: false,
      isError: false,
    });
    const markup = render(<RequestToAddSection variant="grid" query="dune" libraryHadHits />);
    expect(markup).toContain("Result 0");
    expect(markup).toContain("Result 19");
    expect(markup).not.toContain("Result 20");
  });
});
