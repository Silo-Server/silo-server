import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

const mockMarkReadMutate = vi.fn();

vi.mock("@/hooks/queries/notifications", () => ({
  useNotificationsList: vi.fn(() => ({ data: undefined })),
  useMarkRead: vi.fn(() => ({ mutate: mockMarkReadMutate })),
}));

import { useNotificationsList } from "@/hooks/queries/notifications";
import { AnnouncementBar } from "./AnnouncementBar";

const mockedList = vi.mocked(useNotificationsList);

function makeQueryClient() {
  return new QueryClient({ defaultOptions: { queries: { retry: false } } });
}

function renderBar(props: { reserveActivityWidget?: boolean } = {}) {
  const qc = makeQueryClient();
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter>
        <AnnouncementBar {...props} />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

function listResult(items: unknown[]) {
  return {
    data: { pages: [{ items, next_cursor: null }] },
  } as unknown as ReturnType<typeof useNotificationsList>;
}

beforeEach(() => {
  mockMarkReadMutate.mockClear();
  mockedList.mockReturnValue(listResult([]));
});

describe("AnnouncementBar", () => {
  it("renders the latest unread announcement with the ANNOUNCEMENT label", () => {
    mockedList.mockReturnValue(
      listResult([
        {
          id: 7,
          category: "announcement",
          title: "New feature launched",
          body: "Check out the redesign",
          link: "/whats-new",
          read_at: null,
          created_at: "2026-06-11T10:00:00Z",
        },
      ]),
    );

    renderBar();

    expect(screen.getByText("New feature launched")).toBeInTheDocument();
    expect(screen.getByText("ANNOUNCEMENT")).toBeInTheDocument();
  });

  it("dismiss button calls markRead.mutate with the announcement id", async () => {
    mockedList.mockReturnValue(
      listResult([
        {
          id: 7,
          category: "announcement",
          title: "New feature launched",
          body: "Check out the redesign",
          link: "/whats-new",
          read_at: null,
          created_at: "2026-06-11T10:00:00Z",
        },
      ]),
    );

    const user = userEvent.setup();
    renderBar();

    await user.click(screen.getByRole("button", { name: "Dismiss" }));

    expect(mockMarkReadMutate).toHaveBeenCalledWith({ ids: [7] });
  });

  it("renders nothing when there is no unread announcement", () => {
    mockedList.mockReturnValue(listResult([]));

    const { container } = renderBar();

    expect(container).toBeEmptyDOMElement();
  });

  it("shifts the dismiss button with lg:mr-10 when reserveActivityWidget is set", () => {
    mockedList.mockReturnValue(
      listResult([
        {
          id: 7,
          category: "announcement",
          title: "New feature launched",
          body: "Check out the redesign",
          link: "/whats-new",
          read_at: null,
          created_at: "2026-06-11T10:00:00Z",
        },
      ]),
    );

    renderBar({ reserveActivityWidget: true });

    expect(screen.getByRole("button", { name: "Dismiss" })).toHaveClass("lg:mr-10");
  });
});
