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
  useUpdateServerSettings: () => ({ mutateAsync, isPending: false }),
}));

afterEach(() => {
  cleanup();
  mutateAsync.mockReset();
});

describe("useSettingsForm save()", () => {
  it("does not flag a restart when no saved key requires one", async () => {
    mutateAsync.mockResolvedValue({
      values: { "branding.server_name": "Casa" },
      restart_required: false,
    });

    const { result } = renderHook(() => useSettingsForm({ keys: KEYS }));

    act(() => {
      result.current.setValue("branding.server_name", "Casa");
    });
    await act(async () => {
      await result.current.save();
    });

    expect(mutateAsync).toHaveBeenCalledWith({ "branding.server_name": "Casa" });
    expect(result.current.restartRequired).toBe(false);
  });

  it("adopts canonical server values after save", async () => {
    mutateAsync.mockResolvedValue({
      values: { "database.max_connections": "40" },
      restart_required: true,
    });
    const { result } = renderHook(() => useSettingsForm({ keys: KEYS }));

    act(() => {
      result.current.setValue("database.max_connections", " 40 ");
    });
    await act(async () => {
      await result.current.save();
    });

    expect(result.current.getValue("database.max_connections")).toBe("40");
    expect(result.current.dirtyCount).toBe(0);
  });

  it("erases a sensitive draft after the server omits it from the response", async () => {
    mutateAsync.mockResolvedValue({ values: {}, restart_required: false });
    const { result } = renderHook(() => useSettingsForm({ keys: ["email.smtp_password"] }));

    act(() => {
      result.current.setValue("email.smtp_password", "temporary-secret");
    });
    await act(async () => {
      await result.current.save();
    });

    expect(result.current.getValue("email.smtp_password")).toBe("");
    expect(result.current.dirtyCount).toBe(0);
  });

  it("preserves edits made while a save is in flight", async () => {
    let resolveMutation:
      | ((value: { values: Record<string, string>; restart_required: boolean }) => void)
      | undefined;
    mutateAsync.mockReturnValue(
      new Promise((resolve) => {
        resolveMutation = resolve;
      }),
    );
    const { result } = renderHook(() => useSettingsForm({ keys: KEYS }));

    act(() => {
      result.current.setValue("branding.server_name", "Casa");
    });
    let savePromise: Promise<void> | undefined;
    act(() => {
      savePromise = result.current.save();
    });
    act(() => {
      result.current.setValue("branding.server_name", "Villa");
    });
    await act(async () => {
      resolveMutation?.({
        values: { "branding.server_name": "Casa" },
        restart_required: false,
      });
      await savePromise;
    });

    expect(result.current.getValue("branding.server_name")).toBe("Villa");
    expect(result.current.dirtyCount).toBe(1);
  });

  it("flags a restart when any saved key requires one, and keeps it flagged", async () => {
    mutateAsync.mockImplementation((values: Record<string, string>) =>
      Promise.resolve({
        values,
        restart_required: "database.max_connections" in values,
      }),
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
