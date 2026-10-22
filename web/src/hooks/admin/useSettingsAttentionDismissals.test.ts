import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { SettingsAttentionState, SettingsHealthItem } from "./useSettingsHealth";
import {
  settingsAttentionDismissalsKey,
  useSettingsAttentionDismissals,
} from "./useSettingsAttentionDismissals";

const attentionMock = vi.fn<() => SettingsAttentionState>();
const authMock = vi.fn<() => { user: { id: number } | null }>();

vi.mock("./useSettingsHealth", () => ({
  useSettingsAttentionState: () => attentionMock(),
}));
vi.mock("@/hooks/useAuth", () => ({ useAuth: () => authMock() }));

const email: SettingsHealthItem = {
  id: "email",
  label: "Email",
  state: "off",
  stateText: "Not set up",
  detail: "Invites and resets can't send",
  action: { label: "Set up", page: "email" },
};
const publicUrl: SettingsHealthItem = {
  id: "public-url",
  label: "Public URL",
  state: "off",
  stateText: "Not set",
  detail: "Invitation and reset links, and sign-in providers, need it",
  action: { label: "Set up", page: "general" },
};
const storageRestart: SettingsHealthItem = {
  id: "storage",
  label: "Storage",
  state: "warn",
  stateText: "Restart pending",
  detail: "S3 · media · applies after a restart",
  action: { label: "Fix", page: "storage" },
};

const KEY = settingsAttentionDismissalsKey(7);

function setItems(items: SettingsHealthItem[], loaded = true) {
  attentionMock.mockReturnValue({ items, loaded });
}

beforeEach(() => {
  authMock.mockReturnValue({ user: { id: 7 } });
  setItems([email, publicUrl, storageRestart]);
});

afterEach(() => {
  localStorage.clear();
  vi.clearAllMocks();
});

const visibleIds = (result: { current: { visible: SettingsHealthItem[] } }) =>
  result.current.visible.map((item) => item.id);

describe("useSettingsAttentionDismissals", () => {
  it("hides a dismissed item and counts it until everything is restored", () => {
    const { result } = renderHook(() => useSettingsAttentionDismissals());

    act(() => result.current.dismiss(email));
    expect(visibleIds(result)).toEqual(["public-url", "storage"]);
    expect(result.current.hiddenCount).toBe(1);

    act(() => result.current.restoreAll());
    expect(visibleIds(result)).toEqual(["email", "public-url", "storage"]);
    expect(result.current.hiddenCount).toBe(0);
    expect(localStorage.getItem(KEY)).toBeNull();
  });

  it("never hides a broken service or a pending restart", () => {
    const { result } = renderHook(() => useSettingsAttentionDismissals());

    act(() => result.current.dismiss(storageRestart));
    expect(visibleIds(result)).toContain("storage");
    expect(result.current.hiddenCount).toBe(0);
  });

  it("keeps dismissals per account and across remounts", () => {
    const first = renderHook(() => useSettingsAttentionDismissals());
    act(() => first.result.current.dismiss(publicUrl));
    first.unmount();

    expect(visibleIds(renderHook(() => useSettingsAttentionDismissals()).result)).not.toContain(
      "public-url",
    );

    authMock.mockReturnValue({ user: { id: 8 } });
    expect(visibleIds(renderHook(() => useSettingsAttentionDismissals()).result)).toContain(
      "public-url",
    );
  });

  it("hides the item again once a passing restart is done", () => {
    const { result, rerender } = renderHook(() => useSettingsAttentionDismissals());
    act(() => result.current.dismiss(email));

    setItems([{ ...email, state: "warn", stateText: "Restart pending" }, publicUrl]);
    rerender();
    expect(visibleIds(result)).toContain("email");

    setItems([email, publicUrl]);
    rerender();
    expect(visibleIds(result)).toEqual(["public-url"]);
  });

  it("forgets a dismissal once the item resolves, so undoing the setup asks again", () => {
    const { result, rerender } = renderHook(() => useSettingsAttentionDismissals());
    act(() => result.current.dismiss(email));

    setItems([publicUrl]);
    rerender();
    expect(localStorage.getItem(KEY)).toBeNull();

    setItems([email, publicUrl]);
    rerender();
    expect(visibleIds(result)).toContain("email");
  });

  it("keeps dismissals while the attention inputs are still loading", () => {
    const { result, rerender } = renderHook(() => useSettingsAttentionDismissals());
    act(() => result.current.dismiss(email));

    setItems([], false);
    rerender();
    expect(JSON.parse(localStorage.getItem(KEY) ?? "[]")).toEqual(["email"]);

    setItems([email, publicUrl]);
    rerender();
    expect(visibleIds(result)).toEqual(["public-url"]);
  });

  it("follows a dismissal made in another tab", () => {
    const { result } = renderHook(() => useSettingsAttentionDismissals());

    act(() => {
      localStorage.setItem(KEY, JSON.stringify(["public-url"]));
      window.dispatchEvent(new StorageEvent("storage", { key: KEY }));
    });
    expect(visibleIds(result)).not.toContain("public-url");
  });

  it("keeps another tab's dismissal of an item this tab's cache calls resolved", () => {
    // This tab's settings still have email set up; another tab turned it off
    // and hid the notice.
    setItems([publicUrl]);
    const { rerender } = renderHook(() => useSettingsAttentionDismissals());

    act(() => {
      localStorage.setItem(KEY, JSON.stringify(["email"]));
      window.dispatchEvent(new StorageEvent("storage", { key: KEY }));
    });
    setItems([publicUrl]);
    rerender();
    expect(JSON.parse(localStorage.getItem(KEY) ?? "[]")).toEqual(["email"]);
  });
});
