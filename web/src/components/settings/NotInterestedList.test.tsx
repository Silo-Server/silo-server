import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { BrowseItem } from "@/api/types";
import { NotInterestedList } from "@/components/settings/NotInterestedList";

const mocks = vi.hoisted(() => ({ list: vi.fn() }));

vi.mock("@/hooks/queries/notInterested", () => ({
  useNotInterestedList: () => mocks.list(),
  useToggleNotInterested: () => ({ mutate: vi.fn(), isPending: false }),
}));

function title(id: string): BrowseItem {
  return { content_id: id, type: "movie", title: `Title ${id}`, year: 2020 } as BrowseItem;
}

function listState(overrides: Record<string, unknown>) {
  return {
    data: undefined,
    isPending: false,
    isError: false,
    isFetchNextPageError: false,
    hasNextPage: false,
    fetchNextPage: vi.fn(),
    isFetchingNextPage: false,
    refetch: vi.fn(),
    isRefetching: false,
    ...overrides,
  };
}

function renderList() {
  render(
    <MemoryRouter>
      <NotInterestedList />
    </MemoryRouter>,
  );
}

describe("NotInterestedList", () => {
  beforeEach(() => mocks.list.mockReset());

  it("offers more titles when a page holds none the viewer can see", () => {
    mocks.list.mockReturnValue(listState({ data: { pages: [{ items: [] }] }, hasNextPage: true }));
    renderList();
    expect(screen.getByRole("button", { name: "Show more" })).toBeTruthy();
    expect(screen.queryByText(/Nothing yet/)).toBeNull();
  });

  it("keeps the loaded titles when a later page fails", () => {
    mocks.list.mockReturnValue(
      listState({
        data: { pages: [{ items: [title("movie:a")] }] },
        isError: true,
        isFetchNextPageError: true,
        hasNextPage: true,
      }),
    );
    renderList();
    expect(screen.getByText("Title movie:a")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Retry loading more" })).toBeTruthy();
  });

  it("keeps the loaded titles when a refresh fails, with a retry", () => {
    mocks.list.mockReturnValue(
      listState({ data: { pages: [{ items: [title("movie:a")] }] }, isError: true }),
    );
    renderList();
    expect(screen.getByText("Title movie:a")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Retry" })).toBeTruthy();
  });

  it("offers a retry when nothing loaded", () => {
    mocks.list.mockReturnValue(listState({ isError: true }));
    renderList();
    expect(screen.getByText(/Couldn't load/)).toBeTruthy();
    expect(screen.getByRole("button", { name: "Retry" })).toBeTruthy();
  });
});
