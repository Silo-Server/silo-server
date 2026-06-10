// @vitest-environment jsdom

import { act, cleanup, renderHook } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { useSettingsForm } from "./useSettingsForm";

const { mutateAsync } = vi.hoisted(() => ({ mutateAsync: vi.fn() }));

// Stable identities: useSettingsForm's sync effect depends on the settings
// object and keys array (pages memoize keys), so fresh objects per render
// would loop forever.
const KEYS = ["branding.server_name", "database.max_connections"];
const settingsData = { "branding.server_name": "Silo", "database.max_connections": "20" };
const sensitiveData = { configured: [], managed_by_env: [] };

vi.mock("@/hooks/queries/admin/settings", () => ({
  useAdminServerSettings: () => ({ data: settingsData, isLoading: false }),
  useAdminSensitiveStatus: () => ({ data: sensitiveData }),
  useUpdateServerSetting: () => ({ mutateAsync, isPending: false }),
}));

afterEach(() => {
  cleanup();
  mutateAsync.mockReset();
});

describe("useSettingsForm save()", () => {
  it("does not flag a restart when no saved key requires one", async () => {
    mutateAsync.mockResolvedValue({ key: "branding.server_name", restart_required: false });

    const { result } = renderHook(() => useSettingsForm({ keys: KEYS }));

    act(() => {
      result.current.setValue("branding.server_name", "Casa");
    });
    await act(async () => {
      await result.current.save();
    });

    expect(mutateAsync).toHaveBeenCalledWith({ key: "branding.server_name", value: "Casa" });
    expect(result.current.restartRequired).toBe(false);
  });

  it("flags a restart when any saved key requires one, and keeps it flagged", async () => {
    mutateAsync.mockImplementation(({ key }: { key: string }) =>
      Promise.resolve({ key, restart_required: key === "database.max_connections" }),
    );

    const { result } = renderHook(() => useSettingsForm({ keys: KEYS }));

    act(() => {
      result.current.setValue("branding.server_name", "Casa");
      result.current.setValue("database.max_connections", "40");
    });
    await act(async () => {
      await result.current.save();
    });
    expect(result.current.restartRequired).toBe(true);

    // A later save of a live-applied key must not clear the pending restart.
    act(() => {
      result.current.setValue("branding.server_name", "Villa");
    });
    await act(async () => {
      await result.current.save();
    });
    expect(result.current.restartRequired).toBe(true);
  });
});
