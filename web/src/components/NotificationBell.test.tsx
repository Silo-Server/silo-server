import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

const mockMarkReadMutate = vi.fn();

vi.mock("@/hooks/queries/notifications", () => ({
  useUnreadCount: vi.fn(() => ({ data: 0 })),
  useNotificationsList: vi.fn(() => ({ data: undefined })),
  useMarkRead: vi.fn(() => ({ mutate: mockMarkReadMutate })),
}));

// Radix Popover needs a pointer-events-capable env; jsdom provides enough.
// We also need @radix-ui/react-popover to work in jsdom.
// Use the actual Popover so open/close logic fires onOpenChange.

import { useUnreadCount, useNotificationsList } from "@/hooks/queries/notifications";
import { NotificationBell } from "./NotificationBell";

const mockedUnreadCount = vi.mocked(useUnreadCount);
const mockedList = vi.mocked(useNotificationsList);

function makeQueryClient() {
  return new QueryClient({ defaultOptions: { queries: { retry: false } } });
}

function renderBell() {
  const qc = makeQueryClient();
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter>
        <NotificationBell />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  mockMarkReadMutate.mockClear();
  mockedUnreadCount.mockReturnValue({ data: 0 } as ReturnType<typeof useUnreadCount>);
  mockedList.mockReturnValue({ data: undefined } as unknown as ReturnType<typeof useNotificationsList>);
});

describe("NotificationBell", () => {
  describe("badge", () => {
    it("hides badge when unread count is 0", () => {
      mockedUnreadCount.mockReturnValue({ data: 0 } as ReturnType<typeof useUnreadCount>);
      renderBell();
      // Badge should not be rendered at all
      expect(screen.queryByText("0")).not.toBeInTheDocument();
    });

    it("shows badge with count '3' when unread count is 3", () => {
      mockedUnreadCount.mockReturnValue({ data: 3 } as ReturnType<typeof useUnreadCount>);
      renderBell();
      expect(screen.getByText("3")).toBeInTheDocument();
    });

    it("shows badge '99+' when unread count is 120", () => {
      mockedUnreadCount.mockReturnValue({ data: 120 } as ReturnType<typeof useUnreadCount>);
      renderBell();
      expect(screen.getByText("99+")).toBeInTheDocument();
    });
  });

  describe("popover content", () => {
    it("renders item titles and 'View all' link on open", async () => {
      mockedList.mockReturnValue({
        data: {
          pages: [
            {
              items: [
                { id: 1, title: "Movie Released", body: "Dune 3 is out", link: "/item/dune3", read_at: null },
                { id: 2, title: "Request Approved", body: "", link: null, read_at: "2024-01-01" },
              ],
              next_cursor: null,
            },
          ],
        },
      } as unknown as ReturnType<typeof useNotificationsList>);

      const user = userEvent.setup();
      renderBell();

      await user.click(screen.getByRole("button", { name: "Notifications" }));

      expect(screen.getByText("Movie Released")).toBeInTheDocument();
      expect(screen.getByText("Request Approved")).toBeInTheDocument();
      expect(screen.getByText("View all")).toBeInTheDocument();
    });

    it("shows empty state when no items", async () => {
      mockedList.mockReturnValue({
        data: { pages: [{ items: [], next_cursor: null }] },
      } as unknown as ReturnType<typeof useNotificationsList>);

      const user = userEvent.setup();
      renderBell();

      await user.click(screen.getByRole("button", { name: "Notifications" }));

      expect(screen.getByText("You're all caught up")).toBeInTheDocument();
    });

    it("'Mark all read' calls markRead.mutate with { all: true }", async () => {
      mockedUnreadCount.mockReturnValue({ data: 2 } as ReturnType<typeof useUnreadCount>);
      mockedList.mockReturnValue({
        data: { pages: [{ items: [], next_cursor: null }] },
      } as unknown as ReturnType<typeof useNotificationsList>);

      const user = userEvent.setup();
      renderBell();

      await user.click(screen.getByRole("button", { name: "Notifications" }));
      await user.click(screen.getByRole("button", { name: "Mark all read" }));

      expect(mockMarkReadMutate).toHaveBeenCalledWith({ all: true });
    });

    it("item without link routes to /notifications", async () => {
      mockedList.mockReturnValue({
        data: {
          pages: [
            {
              items: [{ id: 3, title: "No Link Item", body: "", link: null, read_at: null }],
              next_cursor: null,
            },
          ],
        },
      } as unknown as ReturnType<typeof useNotificationsList>);

      const user = userEvent.setup();
      renderBell();

      await user.click(screen.getByRole("button", { name: "Notifications" }));

      const link = screen.getByRole("link", { name: /No Link Item/ });
      expect(link).toHaveAttribute("href", "/notifications");
    });

    it("item with link uses provided link", async () => {
      mockedList.mockReturnValue({
        data: {
          pages: [
            {
              items: [{ id: 4, title: "With Link Item", body: "", link: "/item/42", read_at: null }],
              next_cursor: null,
            },
          ],
        },
      } as unknown as ReturnType<typeof useNotificationsList>);

      const user = userEvent.setup();
      renderBell();

      await user.click(screen.getByRole("button", { name: "Notifications" }));

      const link = screen.getByRole("link", { name: /With Link Item/ });
      expect(link).toHaveAttribute("href", "/item/42");
    });

    it("'View all' link points to /notifications", async () => {
      mockedList.mockReturnValue({
        data: { pages: [{ items: [], next_cursor: null }] },
      } as unknown as ReturnType<typeof useNotificationsList>);

      const user = userEvent.setup();
      renderBell();

      await user.click(screen.getByRole("button", { name: "Notifications" }));

      const viewAll = screen.getByRole("link", { name: "View all" });
      expect(viewAll).toHaveAttribute("href", "/notifications");
    });
  });
});
