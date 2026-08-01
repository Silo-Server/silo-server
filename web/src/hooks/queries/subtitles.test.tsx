import { describe, expect, it, vi, beforeEach } from "vitest";
import type { ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";

import { ApiClientError } from "@/api/client";
import { SETTING_KEYS } from "@/lib/settingsContract";
import { resolveSettingValues, type StoredSettingRow } from "@/lib/settingsResolve";
import { buildSubtitleChoiceRequests } from "@/player/utils/subtitleChoicePersistence";
import type { PlayerSubtitleInfo } from "@/player/types";
import { useDeleteSubtitlePreference } from "./subtitles";

const apiMock = vi.hoisted(() => vi.fn());
vi.mock("@/api/client", async () => {
  const actual = await vi.importActual<typeof import("@/api/client")>("@/api/client");
  return { ...actual, api: apiMock };
});

vi.mock("sonner", () => ({ toast: { error: vi.fn(), success: vi.fn() } }));

function createHarness() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
  );
  return { queryClient, wrapper };
}

const TRACKS: PlayerSubtitleInfo[] = [
  {
    index: 0,
    language: "ja",
    codec: "subrip",
    label: "Japanese",
    source: "embedded",
    forced: false,
    hearing_impaired: false,
    url: "",
  },
];

/** The profile_series rows an in-player pick leaves behind. */
function rowsFromInPlayerPick(seriesId: string): StoredSettingRow[] {
  return buildSubtitleChoiceRequests({ seriesId, index: 0, tracks: TRACKS })
    .filter((request) => request.path.startsWith("/settings/values/"))
    .map((request) => ({
      key: decodeURIComponent(request.path.slice("/settings/values/".length).split("?")[0]!),
      scope: "profile_series" as const,
      profileId: "profile-1",
      seriesId,
      value: (request.body as { value: unknown }).value,
    }));
}

describe("useDeleteSubtitlePreference", () => {
  beforeEach(() => {
    apiMock.mockReset();
  });

  it("clears the canonical profile_series rows alongside the legacy row", async () => {
    // Before this, "Auto" deleted only /subtitle-prefs/{id}. profile_series is
    // the first scope in the resolution order for these keys, so the rows an
    // in-player pick left behind kept resolving the abandoned language for
    // every episode of the series, permanently and unreachably.
    const store = rowsFromInPlayerPick("series-1");
    apiMock.mockImplementation((path: string, options?: RequestInit) => {
      if (options?.method !== "DELETE") return Promise.resolve(undefined);
      if (path.startsWith("/subtitle-prefs/")) return Promise.resolve(undefined);
      const key = decodeURIComponent(path.slice("/settings/values/".length).split("?")[0]!);
      expect(path).toContain("scope=profile_series&series_id=series-1");
      const index = store.findIndex((row) => row.key === key);
      if (index < 0) {
        return Promise.reject(
          new ApiClientError(404, "not_found", "No value is set at this scope"),
        );
      }
      store.splice(index, 1);
      return Promise.resolve(undefined);
    });

    const { wrapper } = createHarness();
    const { result } = renderHook(() => useDeleteSubtitlePreference(), { wrapper });

    await result.current.mutateAsync("series-1");
    await waitFor(() => expect(result.current.isSuccess).toBe(true));

    expect(store).toEqual([]);
    expect(apiMock.mock.calls.map(([path]) => path as string)).toContain(
      "/subtitle-prefs/series-1",
    );
    // Resolution falls all the way back to the contract default again.
    const [language] = resolveSettingValues([SETTING_KEYS.PLAYBACK_SUBTITLE_LANGUAGE], store, {
      profileId: "profile-1",
      seriesIds: ["series-1"],
    });
    expect(language?.source).toBe("default");
  });

  it("treats an already-absent canonical row as success", async () => {
    apiMock.mockImplementation((path: string) => {
      if (path.startsWith("/subtitle-prefs/")) return Promise.resolve(undefined);
      return Promise.reject(new ApiClientError(404, "not_found", "No value is set at this scope"));
    });

    const { wrapper } = createHarness();
    const { result } = renderHook(() => useDeleteSubtitlePreference(), { wrapper });

    await expect(result.current.mutateAsync("series-1")).resolves.toBeUndefined();
  });

  it("surfaces a real failure rather than reporting a reset that did not happen", async () => {
    apiMock.mockImplementation((path: string) => {
      if (path.startsWith("/subtitle-prefs/")) return Promise.resolve(undefined);
      return Promise.reject(new ApiClientError(500, "internal_error", "boom"));
    });

    const { wrapper } = createHarness();
    const { result } = renderHook(() => useDeleteSubtitlePreference(), { wrapper });

    await expect(result.current.mutateAsync("series-1")).rejects.toThrow("boom");
  });
});
