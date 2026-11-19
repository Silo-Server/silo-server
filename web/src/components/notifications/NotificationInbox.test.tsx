import { afterEach, expect, it, vi } from "vitest";
import { cleanup, render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import type { AppNotification } from "@/api/types";
import { notificationToast } from "@/hooks/queries/notifications";
import { NotificationInbox } from "./NotificationInbox";

vi.mock("@/hooks/queries/notifications", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/queries/notifications")>()),
  useMarkNotificationsRead: () => ({ mutate: vi.fn(), isPending: false }),
  useSetManyNotificationSeriesMuted: () => ({ mutate: vi.fn(), isPending: false }),
}));

afterEach(cleanup);

const notices = [
  {
    id: "notice-webhook",
    type: "webhook.auto_disabled",
    profile_id: "owner",
    reason_flags: {},
    created_at: new Date().toISOString(),
    read_at: null,
  },
  {
    id: "notice-destination",
    type: "plugin_destination.auto_disabled",
    profile_id: "owner",
    reason_flags: {},
    created_at: new Date(Date.now() - 60_000).toISOString(),
    read_at: null,
  },
] as unknown as AppNotification[];

it("shows a stopped webhook or destination with a link to notification settings", () => {
  const list = {
    isLoading: false,
    isError: false,
    hasNextPage: false,
    data: { pages: [{ notifications: notices }] },
  } as unknown as Parameters<typeof NotificationInbox>[0]["list"];
  render(
    <MemoryRouter>
      <NotificationInbox
        switcher={null}
        status="all"
        onStatusChange={() => {}}
        list={list}
        unreadCount={2}
        markAllRead={{ mutate: vi.fn(), isPending: false } as never}
        canMuteSeries={false}
      />
    </MemoryRouter>,
  );

  for (const title of ["A webhook stopped working", "A notification destination stopped working"]) {
    const link = screen.getByText(title).closest("a");
    expect(link?.getAttribute("href")).toBe("/settings/notifications");
    expect(within(link!).getByText("Open notification settings to fix it.")).toBeTruthy();
  }
});

it("toasts a stopped webhook or destination as the inbox titles it", () => {
  expect(notificationToast(notices[0]!)?.title).toBe("A webhook stopped working");
  expect(notificationToast(notices[1]!)?.title).toBe("A notification destination stopped working");
});
