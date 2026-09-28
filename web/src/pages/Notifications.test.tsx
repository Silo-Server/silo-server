import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import Notifications from "./Notifications";
const state = vi.hoisted(() => ({
  list: {
    data: undefined as unknown,
    isLoading: false,
    isError: false,
    hasNextPage: false,
    restart: vi.fn(),
  },
  all: { mutateAsync: vi.fn(), isPending: false, isError: false, reset: vi.fn() },
}));
vi.mock("@/hooks/useAuth", () => ({ useAuth: () => ({}) }));
vi.mock("@/api/v2/notifications", () => ({ notificationScope: () => "owner" }));
vi.mock("@/hooks/useDocumentTitle", () => ({ useDocumentTitle: () => {} }));
vi.mock("@/hooks/queries/notifications", () => ({
  useNotifications: () => state.list,
  useUnreadNotificationCount: () => ({ data: 2 }),
  useMarkAllNotificationsRead: () => state.all,
  useMarkNotificationRead: () => ({ mutate: vi.fn() }),
  useNotificationPreferences: () => ({ data: undefined, isLoading: false }),
  useUpdateNotificationPreferences: () => ({ mutate: vi.fn() }),
  formatEpisodeCode: () => "",
}));
const row = {
  id: "one",
  type: "episode.available",
  series_title: "Displayed episode",
  created_at: "2026-01-01T00:00:00Z",
  reason_flags: {},
  read_at: null,
};
beforeEach(() => {
  state.list.data = { pages: [{ notifications: [row], read_cutoff: "frozen-cutoff" }] };
  state.list.isError = false;
  state.all.isError = false;
  state.all.isPending = false;
  state.all.mutateAsync.mockReset();
  state.all.reset.mockReset();
  state.list.restart.mockReset().mockResolvedValue(undefined);
});
afterEach(cleanup);
it("submits the displayed cutoff once while the mutation remains pending", async () => {
  let finish!: () => void;
  state.all.mutateAsync.mockReturnValue(
    new Promise<void>((resolve) => {
      finish = resolve;
    }),
  );
  render(<Notifications />);
  const button = screen.getByRole("button", { name: "Mark all read" });
  fireEvent.click(button);
  fireEvent.click(button);
  expect(state.all.mutateAsync).toHaveBeenCalledExactlyOnceWith("frozen-cutoff");
  expect(screen.getByText("Displayed episode")).toBeInTheDocument();
  finish();
});
it("retains partial rows and requires explicit reload after a mark-all failure", async () => {
  state.list.isError = true;
  state.all.isError = true;
  render(<Notifications />);
  expect(screen.getByText("Displayed episode")).toBeInTheDocument();
  expect(screen.queryByText("No notifications yet")).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Mark all read" })).toBeDisabled();
  fireEvent.click(screen.getByRole("button", { name: "Reload inbox" }));
  await waitFor(() => expect(state.all.reset).toHaveBeenCalledOnce());
  expect(state.list.restart).toHaveBeenCalledOnce();
  expect(state.all.mutateAsync).not.toHaveBeenCalled();
});
it("does not describe failed initial loading as an empty inbox or permit an unbounded read-all", () => {
  state.list.data = undefined;
  state.list.isError = true;
  render(<Notifications />);
  expect(screen.queryByText("No notifications yet")).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Mark all read" })).toBeDisabled();
  expect(screen.getByRole("button", { name: "Reload notifications" })).toBeInTheDocument();
});
