import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { storage } from "@/utils/storage";
import { SETTING_DEFINITIONS, SETTINGS_REVISION, type SettingKey } from "@/lib/settingsContract";
import { SEEK_KEYS } from "@/lib/seekIntervals";
import { useSeekPreferences } from "./seekPreferences";

const v2 = vi.hoisted(() => vi.fn());
vi.mock("@/api/v2/request", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/api/v2/request")>()),
  v2,
}));

function harness() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  );
  return { client, wrapper };
}

let revision = SETTINGS_REVISION;
let discoveryFails = false;
let failKey: string | undefined;
let stored: Map<string, number>;

beforeEach(() => {
  const memory = new Map<string, string>();
  Object.defineProperty(globalThis, "localStorage", {
    configurable: true,
    value: {
      getItem: (key: string) => memory.get(key) ?? null,
      setItem: (key: string, value: string) => memory.set(key, value),
      removeItem: (key: string) => memory.delete(key),
    },
  });
  storage.set(storage.KEYS.PROFILE_ID, "p1");
  stored = new Map();
  revision = SETTINGS_REVISION;
  discoveryFails = false;
  failKey = undefined;
  v2.mockReset();
  v2.mockImplementation(
    async (
      operation: string,
      options?: {
        path?: { key: string };
        query?: { keys?: string[]; scope?: string };
        body?: { value: number };
      },
    ) => {
      if (operation.includes("capabilities")) {
        if (discoveryFails) throw new Error("Offline");
        return {
          api_version: 1,
          manifest_revision: revision,
          supports_batched_effective: true,
          supports_idempotent_writes: true,
        };
      }
      const profile = storage.get(storage.KEYS.PROFILE_ID);
      if (operation.endsWith("/effective")) {
        return {
          revision,
          items: options!.query!.keys!.map((key) => ({
            key,
            value:
              stored.get(`${profile}:${key}`) ??
              SETTING_DEFINITIONS[key as SettingKey].defaultValue,
            source: stored.has(`${profile}:${key}`) ? "profile" : "default",
          })),
        };
      }
      const key = options!.path!.key;
      expect(options?.query?.scope).toBe("profile");
      if (key === failKey) throw new Error("Save failed");
      if (operation.startsWith("DELETE ")) stored.delete(`${profile}:${key}`);
      else stored.set(`${profile}:${key}`, options!.body!.value);
      return {};
    },
  );
});

describe("shared seek preferences", () => {
  it("shares profile values across consumers, keeps media separate, and resets", async () => {
    const { wrapper } = harness();
    const { result } = renderHook(
      () => ({
        first: useSeekPreferences("video"),
        second: useSeekPreferences("video"),
        audio: useSeekPreferences("audiobook"),
      }),
      { wrapper },
    );
    await waitFor(() => expect(result.current.first.isLoading).toBe(false));
    expect(result.current.first.skipForward).toBe(30);
    await act(() => result.current.first.save("forward", 90));
    await waitFor(() => expect(result.current.second.skipForward).toBe(90));
    expect(result.current.audio.skipForward).toBe(30);
    await act(() => result.current.first.reset("forward"));
    await waitFor(() => expect(result.current.second.skipForward).toBe(30));
  });

  it("refetches another session's update and isolates a profile switch", async () => {
    const { client, wrapper } = harness();
    const { result, rerender, unmount } = renderHook(() => useSeekPreferences("video"), {
      wrapper,
    });
    await waitFor(() => expect(result.current.isLoading).toBe(false));
    stored.set(`p1:${SEEK_KEYS.video.back}`, 60);
    await act(() => client.invalidateQueries());
    await waitFor(() => expect(result.current.skipBack).toBe(60));
    storage.set(storage.KEYS.PROFILE_ID, "p2");
    rerender();
    expect(result.current.skipBack).toBe(10);
    await waitFor(() => expect(result.current.isLoading).toBe(false));
    unmount();
    storage.set(storage.KEYS.PROFILE_ID, "p1");
    const reloaded = renderHook(() => useSeekPreferences("video"), { wrapper: harness().wrapper });
    await waitFor(() => expect(reloaded.result.current.skipBack).toBe(60));
  });

  it.each(["old", "failed"])(
    "does not read or write unsupported keys after %s discovery",
    async (mode) => {
      revision = mode === "old" ? 8 : SETTINGS_REVISION;
      discoveryFails = mode === "failed";
      const { result } = renderHook(() => useSeekPreferences("video"), {
        wrapper: harness().wrapper,
      });
      await waitFor(() => expect(result.current.isLoading).toBe(false));
      expect(result.current.supported).toBe(false);
      // Both settled outcomes release the browser-local path: an unreachable
      // capabilities endpoint must not lock a working audiobook player.
      expect(result.current.legacyAvailable).toBe(true);
      await expect(result.current.save("back", 15)).rejects.toThrow("unavailable");
      expect(v2.mock.calls.every(([operation]) => operation.includes("capabilities"))).toBe(true);
    },
  );

  it("imports only explicitly, reports partial failure, and retains legacy values", async () => {
    storage.set(storage.KEYS.AUDIOBOOK_SKIP_BACK, "15");
    storage.set(storage.KEYS.AUDIOBOOK_SKIP_FORWARD, "60");
    const { result } = renderHook(() => useSeekPreferences("audiobook"), {
      wrapper: harness().wrapper,
    });
    await waitFor(() => expect(result.current.isLoading).toBe(false));
    expect(result.current.legacy).toEqual({ back: 15, forward: 60 });
    expect(result.current.skipBack).toBe(10);
    expect(stored.size).toBe(0);
    failKey = SEEK_KEYS.audiobook.forward;
    let results: Awaited<ReturnType<typeof result.current.importLegacy>> = [];
    await act(async () => {
      results = await result.current.importLegacy();
    });
    expect(results.map(({ direction, status }) => [direction, status])).toEqual([
      ["back", "fulfilled"],
      ["forward", "rejected"],
    ]);
    await waitFor(() => expect(result.current.skipBack).toBe(15));
    expect(result.current.skipForward).toBe(30);
    expect(storage.get(storage.KEYS.AUDIOBOOK_SKIP_FORWARD)).toBe("60");
    failKey = undefined;
    await act(() => result.current.importLegacy());
    await waitFor(() => expect(result.current.skipForward).toBe(60));
  });

  it("rejects invalid intervals and leaves a failed save out of the resolved value", async () => {
    const { result } = renderHook(() => useSeekPreferences("video"), {
      wrapper: harness().wrapper,
    });
    await waitFor(() => expect(result.current.isLoading).toBe(false));
    await expect(result.current.save("back", 7)).rejects.toThrow("supported seek interval");
    failKey = SEEK_KEYS.video.back;
    await act(async () => {
      await expect(result.current.save("back", 15)).rejects.toThrow("Save failed");
    });
    expect(result.current.skipBack).toBe(10);
  });
});
