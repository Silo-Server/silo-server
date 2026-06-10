import { renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const { navigateMock } = vi.hoisted(() => ({ navigateMock: vi.fn() }));

vi.mock("react-router", () => ({ useNavigate: () => navigateMock }));

import { useServiceWorkerNavigation } from "./useServiceWorkerNavigation";

// jsdom has no ServiceWorkerContainer; stand in a real EventTarget so the hook
// can attach/detach its "message" listener and we can dispatch synthetic events.
const fakeServiceWorker = new EventTarget();
Object.defineProperty(navigator, "serviceWorker", {
  configurable: true,
  value: fakeServiceWorker,
});

beforeEach(() => {
  navigateMock.mockClear();
});

const message = (data: unknown) => new MessageEvent("message", { data });

describe("useServiceWorkerNavigation", () => {
  it("navigates the SPA on a notification-click message", () => {
    renderHook(() => useServiceWorkerNavigation());
    fakeServiceWorker.dispatchEvent(message({ type: "notification-click", link: "/item/42" }));
    expect(navigateMock).toHaveBeenCalledWith("/item/42");
  });

  it("ignores unrelated message types", () => {
    renderHook(() => useServiceWorkerNavigation());
    fakeServiceWorker.dispatchEvent(message({ type: "something-else", link: "/item/42" }));
    expect(navigateMock).not.toHaveBeenCalled();
  });

  it("ignores notification-click messages without a link", () => {
    renderHook(() => useServiceWorkerNavigation());
    fakeServiceWorker.dispatchEvent(message({ type: "notification-click" }));
    expect(navigateMock).not.toHaveBeenCalled();
  });

  it("removes the listener on unmount", () => {
    const { unmount } = renderHook(() => useServiceWorkerNavigation());
    unmount();
    fakeServiceWorker.dispatchEvent(message({ type: "notification-click", link: "/item/42" }));
    expect(navigateMock).not.toHaveBeenCalled();
  });
});
