import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import type { Library } from "@/api/types";
import { MetadataMatcherQueuesSection } from "./MetadataMatcherQueuesSection";

const mocks = vi.hoisted(() => ({
  useQueues: vi.fn(),
  useDetail: vi.fn(),
  useRetry: vi.fn(),
}));

vi.mock("@/hooks/queries/admin/libraries", () => ({
  useLibraryMetadataMatchQueues: (...args: unknown[]) => mocks.useQueues(...args),
  useLibraryMetadataMatchQueueDetail: (...args: unknown[]) => mocks.useDetail(...args),
  useRetryLibraryMetadataMatchQueue: (...args: unknown[]) => mocks.useRetry(...args),
}));

describe("MetadataMatcherQueuesSection", () => {
  it("renders the structured failure kind with the parked item detail", async () => {
    mocks.useQueues.mockReturnValue({
      data: [
        {
          library_id: 1,
          movie_count: 1,
          series_count: 0,
          raw_file_count: 0,
          total_count: 1,
          pending_count: 0,
          parked_count: 1,
        },
      ],
    });
    mocks.useDetail.mockReturnValue({
      data: {
        pages: [
          {
            movies: [
              {
                media_file_id: 42,
                file_path: "/media/movies/Unknown.mkv",
                state: "parked",
                failure_kind: "candidate_rejected",
                failure_detail: { message: "Score below threshold" },
              },
            ],
            series: [],
            raw_files: [],
          },
        ],
      },
      hasNextPage: false,
      fetchNextPage: vi.fn(),
    });
    mocks.useRetry.mockReturnValue({ mutate: vi.fn(), isPending: false, variables: undefined });
    const libraries = [{ id: 1, name: "Movies" }] as Library[];

    render(<MetadataMatcherQueuesSection libraries={libraries} />);
    await userEvent.click(screen.getByRole("button", { name: /metadata matcher/i }));
    await userEvent.click(screen.getByText("Movies"));

    expect(screen.getByText("candidate rejected")).toBeInTheDocument();
    expect(screen.getByText("Score below threshold")).toBeInTheDocument();
    expect(screen.getByText("parked")).toBeInTheDocument();
  });

  it("pages through every queue entry instead of stopping at the first ten", async () => {
    mocks.useQueues.mockReturnValue({
      data: [
        {
          library_id: 1,
          movie_count: 15,
          series_count: 0,
          raw_file_count: 0,
          total_count: 15,
          pending_count: 15,
          parked_count: 0,
        },
      ],
    });
    const pageOf = (offset: number, count: number) => ({
      limit: 10,
      movie_count: 15,
      series_count: 0,
      raw_file_count: 0,
      movies: Array.from({ length: count }, (_, index) => ({
        media_file_id: offset + index + 1,
        file_path: `/media/movies/Movie ${offset + index + 1}.mkv`,
        state: "pending",
      })),
      series: [],
      raw_files: [],
    });
    // The query starts with one loaded page; fetchNextPage appends the second
    // and the hook re-renders with both pages retained.
    let pages = [pageOf(0, 10)];
    const fetchNextPage = vi.fn(async () => {
      pages = [...pages, pageOf(10, 5)];
      return { data: { pages } };
    });
    mocks.useDetail.mockImplementation(() => ({
      data: { pages },
      isFetching: false,
      hasNextPage: pages.length < 2,
      fetchNextPage,
    }));
    mocks.useRetry.mockReturnValue({ mutate: vi.fn(), isPending: false, variables: undefined });
    const libraries = [{ id: 1, name: "Movies" }] as Library[];

    render(<MetadataMatcherQueuesSection libraries={libraries} />);
    await userEvent.click(screen.getByRole("button", { name: /metadata matcher/i }));
    await userEvent.click(screen.getByText("Movies"));
    expect(mocks.useDetail).toHaveBeenLastCalledWith(1);
    expect(screen.getByText("/media/movies/Movie 10.mkv")).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Next" }));

    expect(fetchNextPage).toHaveBeenCalledTimes(1);
    expect(await screen.findByText("Page 2")).toBeInTheDocument();
    expect(screen.getByText("/media/movies/Movie 15.mkv")).toBeInTheDocument();

    // Going back reads the retained first page without another fetch.
    await userEvent.click(screen.getByRole("button", { name: "Previous" }));
    expect(screen.getByText("/media/movies/Movie 1.mkv")).toBeInTheDocument();
    expect(fetchNextPage).toHaveBeenCalledTimes(1);
  });
});
