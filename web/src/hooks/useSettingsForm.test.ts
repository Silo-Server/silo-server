// @vitest-environment jsdom

import { act, cleanup, renderHook } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { useHasUnsavedChanges } from "./useUnsavedChanges";
import { useSettingsForm } from "./useSettingsForm";
import { useStepSubmit } from "@/pages/setup-wizard/useStep";

const { mutateAsync, markDone, snapshot, authority } = vi.hoisted(() => ({
  mutateAsync: vi.fn(),
  markDone: vi.fn(),
  snapshot: { loaded: true },
  authority: { profileId: "profile-a" },
}));
vi.mock("@/pages/setup-wizard/WizardContext", () => ({
  useWizardContext: () => ({ markDone }),
}));
vi.mock("@/api/client", () => ({
  captureProfileRequestContext: () => ({
    serverOrigin: "https://example.invalid",
    authContextVersion: 1,
    profileId: authority.profileId,
  }),
}));

// Stable identities: useSettingsForm's sync effect depends on the settings
// object and keys array (pages memoize keys), so fresh objects per render
// would loop forever.
const KEYS = ["branding.server_name", "database.max_connections"];
const settingsData = { "branding.server_name": "Silo", "database.max_connections": "20" };
const sensitiveData = { configured: [], managed_by_env: [] };

vi.mock("@/hooks/queries/admin/settings", () => ({
  useAdminServerSettings: () => ({
    data: snapshot.loaded ? settingsData : undefined,
    isLoading: false,
  }),
  useAdminSensitiveStatus: () => ({ data: sensitiveData }),
  useUpdateServerSettings: () => ({ mutateAsync, isPending: false }),
}));

afterEach(() => {
  cleanup();
  mutateAsync.mockReset();
  markDone.mockReset();
  snapshot.loaded = true;
  authority.profileId = "profile-a";
});

describe("useSettingsForm save()", () => {
  it("rejects a failed write and keeps the staged draft for retry", async () => {
    const error = new Error("412 precondition failed");
    mutateAsync.mockRejectedValue(error);
    const { result } = renderHook(() => useSettingsForm({ keys: KEYS }));
    act(() => result.current.setValue("branding.server_name", "Casa"));

    await act(async () => {
      await expect(result.current.save()).rejects.toBe(error);
    });

    expect(result.current.getValue("branding.server_name")).toBe("Casa");
    expect(result.current.dirtyCount).toBe(1);
    expect(result.current.restartRequired).toBe(false);
  });

  it("rejects saving before a settings snapshot has loaded", async () => {
    snapshot.loaded = false;
    const { result } = renderHook(() => useSettingsForm({ keys: KEYS }));
    act(() => result.current.setValue("branding.server_name", "Casa"));

    await act(async () => {
      await expect(result.current.save()).rejects.toThrow("Reload settings before saving.");
    });

    expect(mutateAsync).not.toHaveBeenCalled();
    expect(result.current.dirtyCount).toBe(1);
  });

  it.each([false, true])(
    "advances the wizard only after a successful save (failure=%s)",
    async (failure) => {
      if (failure) mutateAsync.mockRejectedValue(new Error("412 precondition failed"));
      else
        mutateAsync.mockResolvedValue({
          values: { "branding.server_name": "Casa" },
          restart_required: false,
        });
      const { result } = renderHook(() => {
        const form = useSettingsForm({ keys: KEYS });
        return { form, step: useStepSubmit("server", form, "Save failed") };
      });
      act(() => result.current.form.setValue("branding.server_name", "Casa"));
      await act(async () => {
        await result.current.step.handleSubmit({ preventDefault() {} } as Parameters<
          typeof result.current.step.handleSubmit
        >[0]);
      });

      if (failure) expect(markDone).not.toHaveBeenCalled();
      else expect(markDone).toHaveBeenCalledWith("server");
      expect(result.current.form.dirtyCount).toBe(failure ? 1 : 0);
    },
  );

  it("clears staged secrets when the acting profile changes", () => {
    const { result, rerender } = renderHook(() =>
      useSettingsForm({ keys: ["email.smtp_password"] }),
    );
    act(() => result.current.setValue("email.smtp_password", "draft-secret"));
    expect(result.current.dirtyCount).toBe(1);
    authority.profileId = "profile-b";
    rerender();
    expect(result.current.getValue("email.smtp_password")).toBe("");
    expect(result.current.dirtyCount).toBe(0);
    expect(result.current.restartRequired).toBe(false);
  });

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

  it("can save selected dirty keys without clearing the remaining draft", async () => {
    mutateAsync.mockResolvedValue({
      values: { "branding.server_name": "Casa" },
      restart_required: false,
    });
    const { result } = renderHook(() => useSettingsForm({ keys: KEYS }));

    act(() => {
      result.current.setValue("branding.server_name", "Casa");
      result.current.setValue("database.max_connections", "40");
    });
    await act(async () => {
      await result.current.save(["branding.server_name"]);
    });

    expect(mutateAsync).toHaveBeenCalledWith({ "branding.server_name": "Casa" });
    expect(result.current.getValue("database.max_connections")).toBe("40");
    expect(result.current.dirtyKeys).toEqual(["database.max_connections"]);
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

describe("useSettingsForm isClearStaged()", () => {
  it("separates a staged clear from an untouched or replaced value", () => {
    const { result } = renderHook(() => useSettingsForm({ keys: KEYS }));

    // An untouched empty key is not a clear: nothing would be written.
    expect(result.current.isClearStaged("email.smtp_password")).toBe(false);

    act(() => {
      result.current.setValue("branding.server_name", "");
    });
    expect(result.current.isClearStaged("branding.server_name")).toBe(true);

    act(() => {
      result.current.setValue("branding.server_name", "Casa");
    });
    expect(result.current.isClearStaged("branding.server_name")).toBe(false);

    act(() => {
      result.current.resetValue("branding.server_name");
    });
    expect(result.current.isClearStaged("branding.server_name")).toBe(false);
  });
});

describe("useSettingsForm unsaved-changes guard", () => {
  // What `UnsavedChangesGuard` reads to block in-app navigation. The hook keeps
  // no router dependency of its own; it only reports.
  it("publishes staged edits to the shared unsaved-changes registry", () => {
    const registry = renderHook(() => useHasUnsavedChanges());
    const form = renderHook(() => useSettingsForm({ keys: KEYS }));

    expect(registry.result.current).toBe(false);

    act(() => {
      form.result.current.setValue("branding.server_name", "Casa");
    });
    expect(registry.result.current).toBe(true);

    act(() => {
      form.result.current.discard();
    });
    expect(registry.result.current).toBe(false);
  });

  it("withdraws its registry claim when the form unmounts", () => {
    const registry = renderHook(() => useHasUnsavedChanges());
    const form = renderHook(() => useSettingsForm({ keys: KEYS }));

    act(() => {
      form.result.current.setValue("branding.server_name", "Casa");
    });
    expect(registry.result.current).toBe(true);

    form.unmount();
    expect(registry.result.current).toBe(false);
  });
});
