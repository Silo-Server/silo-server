import { describe, expect, it, vi, beforeEach } from "vitest";
import type { ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";

import { v2Problem } from "@/api/v2/problems.test-support";
import { DEFAULT_SUBTITLE_APPEARANCE } from "@/lib/subtitleAppearance";
import { SETTING_KEYS } from "@/lib/settingsContract";
import { resolveSettingValues, type StoredSettingRow } from "@/lib/settingsResolve";
import { useSubtitleAppearanceSetting } from "./subtitleAppearance";

const v2Mock = vi.hoisted(() => vi.fn());
vi.mock("@/api/v2/request", async () => {
  const actual = await vi.importActual<typeof import("@/api/v2/request")>("@/api/v2/request");
  return { ...actual, v2: v2Mock };
});

const KEY = SETTING_KEYS.PLAYBACK_SUBTITLE_APPEARANCE;

function createHarness() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
  );
  return { queryClient, wrapper };
}

/**
 * A server stand-in that resolves through the shared client resolver, which
 * mirrors internal/settingsresolve, so a profile_device row really does
 * outrank a profile row here.
 */
function fakeSettingsServer(initial: StoredSettingRow[] = []) {
  const rows = [...initial];
  v2Mock.mockImplementation(
    (
      operation: string,
      options?: { path?: { key: string }; query?: { scope: string }; body?: { value: unknown } },
    ) => {
      if (operation === "GET /api/v2/settings/values/effective") {
        const resolved = resolveSettingValues([KEY], rows, {
          profileId: "profile-1",
          deviceId: "device-1",
        })[0]!;
        return Promise.resolve({
          revision: 1,
          items: [
            { key: KEY, value: resolved.value, source: resolved.source, definition_revision: 1 },
          ],
        });
      }
      const key = options?.path?.key;
      const scope = options?.query?.scope;
      if (!key || !scope) return Promise.resolve(undefined);
      const index = rows.findIndex((row) => row.key === key && row.scope === scope);
      if (operation.startsWith("DELETE ")) {
        if (index < 0) {
          return Promise.reject(v2Problem(404, "not_found", "No value is set at this scope"));
        }
        rows.splice(index, 1);
        return Promise.resolve(undefined);
      }
      const row: StoredSettingRow = {
        key,
        scope: scope as StoredSettingRow["scope"],
        profileId: "profile-1",
        deviceId: scope === "profile_device" ? "device-1" : undefined,
        value: options?.body?.value,
      };
      if (index < 0) rows.push(row);
      else rows[index] = row;
      return Promise.resolve(undefined);
    },
  );
  return rows;
}

const XLARGE = { ...DEFAULT_SUBTITLE_APPEARANCE, fontSize: "xlarge" as const };

describe("useSubtitleAppearanceSetting", () => {
  beforeEach(() => {
    v2Mock.mockReset();
  });

  it("saves at profile scope so the appearance follows the profile to every device", async () => {
    const rows = fakeSettingsServer();
    const { wrapper } = createHarness();
    const { result } = renderHook(() => useSubtitleAppearanceSetting(), { wrapper });
    await waitFor(() => expect(result.current.isLoading).toBe(false));

    await result.current.save(XLARGE);

    // The contract types this value as an object, so the body carries the
    // object itself rather than a JSON-encoded string.
    expect(rows).toEqual([expect.objectContaining({ key: KEY, scope: "profile", value: XLARGE })]);
  });

  it("clears this browser's row so a save from an older per-device build stops winning", async () => {
    const rows = fakeSettingsServer([
      {
        key: KEY,
        scope: "profile_device",
        profileId: "profile-1",
        deviceId: "device-1",
        value: { ...DEFAULT_SUBTITLE_APPEARANCE, fontSize: "small" },
      },
    ]);
    const { wrapper } = createHarness();
    const { result } = renderHook(() => useSubtitleAppearanceSetting(), { wrapper });
    await waitFor(() => expect(result.current.appearance.fontSize).toBe("small"));

    await result.current.save(XLARGE);

    expect(rows).toEqual([expect.objectContaining({ scope: "profile", value: XLARGE })]);
    await waitFor(() => expect(result.current.appearance.fontSize).toBe("xlarge"));
  });

  it("does not attempt a delete when nothing is shadowing the profile row", async () => {
    fakeSettingsServer([{ key: KEY, scope: "profile", profileId: "profile-1", value: XLARGE }]);
    const { wrapper } = createHarness();
    const { result } = renderHook(() => useSubtitleAppearanceSetting(), { wrapper });
    await waitFor(() => expect(result.current.appearance.fontSize).toBe("xlarge"));

    await result.current.save(DEFAULT_SUBTITLE_APPEARANCE);

    const deletes = v2Mock.mock.calls.filter(([operation]) =>
      String(operation).startsWith("DELETE "),
    );
    expect(deletes).toHaveLength(0);
  });

  it("resets to the default by clearing both the profile and this browser's rows", async () => {
    const rows = fakeSettingsServer([
      { key: KEY, scope: "profile", profileId: "profile-1", value: XLARGE },
      {
        key: KEY,
        scope: "profile_device",
        profileId: "profile-1",
        deviceId: "device-1",
        value: XLARGE,
      },
    ]);
    const { wrapper } = createHarness();
    const { result } = renderHook(() => useSubtitleAppearanceSetting(), { wrapper });
    await waitFor(() => expect(result.current.isCustomized).toBe(true));

    await result.current.reset();

    expect(rows).toEqual([]);
    await waitFor(() => expect(result.current.isCustomized).toBe(false));
    expect(result.current.appearance).toEqual(DEFAULT_SUBTITLE_APPEARANCE);
  });

  it("treats a reset with nothing stored as already done", async () => {
    fakeSettingsServer();
    const { wrapper } = createHarness();
    const { result } = renderHook(() => useSubtitleAppearanceSetting(), { wrapper });
    await waitFor(() => expect(result.current.isLoading).toBe(false));

    // The canonical DELETE answers 404 for "nothing stored here", which for a
    // reset is the requested state rather than a failure.
    await expect(result.current.reset()).resolves.toBeUndefined();
  });
});
