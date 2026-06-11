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
import { NotificationStrip } from "./NotificationStrip";

const mockedList = vi.mocked(useNotificationsList);

function makeQueryClient() {
  return new QueryClient({ defaultOptions: { queries: { retry: false } } });
}

function renderStrip() {
  const qc = makeQueryClient();
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter>
        <NotificationStrip />
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

describe("NotificationStrip", () => {
  it("skips a leading announcement and shows the first non-announcement", () => {
    mockedList.mockReturnValue(
      listResult([
        {
          id: 1,
          category: "announcement",
          title: "Server maintenance",
          body: "Down at midnight",
          link: null,
          read_at: null,
          created_at: "2026-06-11T10:00:00Z",
        },
        {
          id: 2,
          category: "request",
          title: "Request approved",
          body: "Dune 3 is on the way",
          link: "/requests/2",
          read_at: null,
          created_at: "2026-06-11T09:00:00Z",
        },
      ]),
    );

    renderStrip();

    expect(screen.getByText("Request approved")).toBeInTheDocument();
    expect(screen.queryByText("Server maintenance")).not.toBeInTheDocument();
  });

  it("dismiss button calls markRead.mutate with the item id", async () => {
    mockedList.mockReturnValue(
      listResult([
        {
          id: 2,
          category: "request",
          title: "Request approved",
          body: "Dune 3 is on the way",
          link: "/requests/2",
          read_at: null,
          created_at: "2026-06-11T09:00:00Z",
        },
      ]),
    );

    const user = userEvent.setup();
    renderStrip();

    await user.click(screen.getByRole("button", { name: "Dismiss" }));

    expect(mockMarkReadMutate).toHaveBeenCalledWith({ ids: [2] });
  });

  it("renders nothing when the only unread is an announcement", () => {
    mockedList.mockReturnValue(
      listResult([
        {
          id: 1,
          category: "announcement",
          title: "Server maintenance",
          body: "Down at midnight",
          link: null,
          read_at: null,
          created_at: "2026-06-11T10:00:00Z",
        },
      ]),
    );

    const { container } = renderStrip();

    expect(container).toBeEmptyDOMElement();
  });

  it("renders nothing when there are no unread notifications", () => {
    mockedList.mockReturnValue(listResult([]));

    const { container } = renderStrip();

    expect(container).toBeEmptyDOMElement();
  });
});
