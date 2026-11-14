import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { SettingsAttentionState, SettingsHealthItem } from "./useSettingsHealth";
import {
  SETTINGS_ATTENTION_HIDDEN_KEY as KEY,
  useSettingsAttentionDismissals,
} from "./useSettingsAttentionDismissals";

const attentionMock = vi.fn<() => SettingsAttentionState>();
// The server's settings map; a save replaces it, as the refetch would.
let settings: Record<string, string> = {};
const writes: string[] = [];
let fetching = 0;

vi.mock("@tanstack/react-query", () => ({ useIsFetching: () => fetching }));
vi.mock("./useSettingsHealth", () => ({
  useSettingsAttentionState: () => attentionMock(),
}));
vi.mock("@/hooks/queries/admin/settings", () => ({
  useAdminServerSettings: () => ({ data: settings }),
  useUpdateServerSetting: () => ({
    isPending: false,
    variables: undefined,
    mutate: ({ key, value }: { key: string; value: string }) => {
      writes.push(value);
      settings = { ...settings, [key]: value };
    },
  }),
}));

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

function setItems(items: SettingsHealthItem[], loaded = true) {
  attentionMock.mockReturnValue({ items, loaded });
}

beforeEach(() => {
  settings = {};
  fetching = 0;
  setItems([email, publicUrl, storageRestart]);
});

afterEach(() => {
  writes.length = 0;
  vi.clearAllMocks();
});

const visibleIds = (result: { current: { visible: SettingsHealthItem[] } }) =>
  result.current.visible.map((item) => item.id);

describe("useSettingsAttentionDismissals", () => {
  it("hides a dismissed item and counts it until everything is restored", () => {
    const { result, rerender } = renderHook(() => useSettingsAttentionDismissals());

    act(() => result.current.dismiss(email));
    rerender();
    expect(settings[KEY]).toBe('["email"]');
    expect(visibleIds(result)).toEqual(["public-url", "storage"]);
    expect(result.current.hiddenCount).toBe(1);

    act(() => result.current.restoreAll());
    rerender();
    expect(visibleIds(result)).toEqual(["email", "public-url", "storage"]);
    expect(result.current.hiddenCount).toBe(0);
    expect(settings[KEY]).toBe("");
  });

  it("never hides a broken service or a pending restart", () => {
    settings = { [KEY]: '["storage"]' };
    const { result } = renderHook(() => useSettingsAttentionDismissals());

    act(() => result.current.dismiss(storageRestart));
    expect(writes).toEqual([]);
    expect(visibleIds(result)).toContain("storage");
    expect(result.current.hiddenCount).toBe(0);
  });

  it("hides the item again once a passing restart is done", () => {
    settings = { [KEY]: '["email"]' };
    const { result, rerender } = renderHook(() => useSettingsAttentionDismissals());

    setItems([{ ...email, state: "warn", stateText: "Restart pending" }, publicUrl]);
    rerender();
    expect(visibleIds(result)).toContain("email");

    setItems([email, publicUrl]);
    rerender();
    expect(visibleIds(result)).toEqual(["public-url"]);
    expect(writes).toEqual([]);
  });

  it("forgets a dismissal once the item resolves, so undoing the setup asks again", () => {
    settings = { [KEY]: '["email","public-url"]' };
    const { result, rerender } = renderHook(() => useSettingsAttentionDismissals());

    setItems([publicUrl]);
    rerender();
    expect(settings[KEY]).toBe('["public-url"]');

    setItems([email, publicUrl]);
    rerender();
    expect(visibleIds(result)).toEqual(["email"]);
  });

  it("prunes again when the same list comes back while the Dashboard stays open", () => {
    settings = { [KEY]: '["email"]' };
    setItems([publicUrl]);
    const { result, rerender } = renderHook(() => useSettingsAttentionDismissals());
    expect(settings[KEY]).toBe("");

    setItems([email, publicUrl]);
    rerender();
    act(() => result.current.dismiss(email));
    rerender();
    expect(settings[KEY]).toBe('["email"]');

    setItems([publicUrl]);
    rerender();
    expect(settings[KEY]).toBe("");
  });

  it("waits for settings and server status to finish refetching before pruning", () => {
    settings = { [KEY]: '["email"]' };
    fetching = 1;
    setItems([publicUrl]);
    const { rerender } = renderHook(() => useSettingsAttentionDismissals());
    expect(writes).toEqual([]);

    fetching = 0;
    rerender();
    expect(settings[KEY]).toBe("");
  });

  it("keeps dismissals while the attention inputs are still loading", () => {
    settings = { [KEY]: '["email"]' };
    setItems([], false);
    const { result, rerender } = renderHook(() => useSettingsAttentionDismissals());
    expect(writes).toEqual([]);

    setItems([email, publicUrl]);
    rerender();
    expect(visibleIds(result)).toEqual(["public-url"]);
  });
});
