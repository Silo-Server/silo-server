import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook } from "@testing-library/react";
import { describe, expect, it, vi, beforeEach } from "vitest";

const { capturedHandlers, toastMock, profileState, bellState } = vi.hoisted(() => ({
  capturedHandlers: {} as { onEvent?: (m: unknown) => void; onSnapshot?: (m: unknown) => void },
  toastMock: Object.assign(vi.fn(), { success: vi.fn(), error: vi.fn() }),
  profileState: {
    profile: { id: "p-1", is_child: false } as { id: string; is_child: boolean } | null,
  },
  bellState: { open: false },
}));

vi.mock("@/components/NotificationBell", () => ({
  isNotificationDropdownOpen: () => bellState.open,
  setNotificationDropdownOpenForTests: (v: boolean) => {
    bellState.open = v;
  },
}));
vi.mock("@/components/realtimeEventsContext", () => ({
  useEventChannel: (
    _c: string,
    handlers?: { onEvent?: (m: unknown) => void; onSnapshot?: (m: unknown) => void },
  ) => {
    capturedHandlers.onEvent = handlers?.onEvent;
    capturedHandlers.onSnapshot = handlers?.onSnapshot;
  },
}));
vi.mock("sonner", () => ({ toast: toastMock }));
vi.mock("@/hooks/useAuth", () => ({ useAuth: () => profileState }));

import { notificationKeys } from "@/hooks/queries/keys";
import { useNotificationsLive } from "./useNotificationsLive";

function setup(initialCount = 1) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  queryClient.setQueryData(notificationKeys.unreadCount(), { count: initialCount });
  renderHook(() => useNotificationsLive(), {
    wrapper: ({ children }) => (
      <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
    ),
  });
  return queryClient;
}

const frame = (data: object) => ({
  type: "event",
  channel: "notifications",
  event: "notification.created",
  data,
});

beforeEach(() => {
  toastMock.mockClear();
  profileState.profile = { id: "p-1", is_child: false };
  bellState.open = false;
});

describe("useNotificationsLive", () => {
  it("toasts and bumps unread count on notification.created", () => {
    const qc = setup(1);
    capturedHandlers.onEvent?.(
      frame({
        id: 5,
        category: "request",
        title: "Request approved",
        body: "Dune",
        profile_id: "p-1",
      }),
    );
    expect(toastMock).toHaveBeenCalledWith(
      "Request approved",
      expect.objectContaining({ description: "Dune" }),
    );
    expect(qc.getQueryData(notificationKeys.unreadCount())).toEqual({ count: 2 });
  });

  it("counts user-wide frames (no profile_id)", () => {
    const qc = setup(0);
    capturedHandlers.onEvent?.(
      frame({ id: 8, category: "announcement", title: "Maintenance", body: "" }),
    );
    expect(qc.getQueryData(notificationKeys.unreadCount())).toEqual({ count: 1 });
  });

  it("ignores frames for another profile", () => {
    const qc = setup(1);
    capturedHandlers.onEvent?.(
      frame({ id: 6, category: "content", title: "New content", body: "", profile_id: "p-other" }),
    );
    expect(toastMock).not.toHaveBeenCalled();
    expect(qc.getQueryData(notificationKeys.unreadCount())).toEqual({ count: 1 });
  });

  it("suppresses restricted categories on child profiles", () => {
    profileState.profile = { id: "p-1", is_child: true };
    setup(0);
    capturedHandlers.onEvent?.(
      frame({ id: 7, category: "request", title: "Request approved", body: "" }),
    );
    expect(toastMock).not.toHaveBeenCalled();
  });

  it("ignores non-created events", () => {
    setup(0);
    capturedHandlers.onEvent?.({
      type: "event",
      channel: "notifications",
      event: "something.else",
      data: { id: 1 },
    });
    expect(toastMock).not.toHaveBeenCalled();
  });

  it("refetches active notification lists when an announcement arrives", () => {
    const qc = setup(0);
    const spy = vi.spyOn(qc, "invalidateQueries");
    capturedHandlers.onEvent?.(
      frame({
        id: 11,
        category: "announcement",
        title: "Maintenance",
        body: "",
        profile_id: "p-1",
      }),
    );
    expect(spy).toHaveBeenCalledWith(expect.objectContaining({ refetchType: "active" }));
  });

  it("only marks lists stale (no refetch) for routine notifications", () => {
    const qc = setup(0);
    const spy = vi.spyOn(qc, "invalidateQueries");
    capturedHandlers.onEvent?.(
      frame({ id: 12, category: "request", title: "Approved", body: "", profile_id: "p-1" }),
    );
    expect(spy).toHaveBeenCalledWith(expect.objectContaining({ refetchType: "none" }));
  });

  it("seeds unread count from snapshot", () => {
    const qc = setup(1);
    capturedHandlers.onSnapshot?.({
      type: "snapshot",
      channel: "notifications",
      data: { unread_count: 9 },
    });
    expect(qc.getQueryData(notificationKeys.unreadCount())).toEqual({ count: 9 });
  });

  it("suppresses toast but still bumps unread count while bell dropdown is open", () => {
    bellState.open = true;
    const qc = setup(1);
    capturedHandlers.onEvent?.(
      frame({
        id: 10,
        category: "content",
        title: "New Release",
        body: "Arrival",
        profile_id: "p-1",
      }),
    );
    expect(toastMock).not.toHaveBeenCalled();
    expect(qc.getQueryData(notificationKeys.unreadCount())).toEqual({ count: 2 });
  });
});
