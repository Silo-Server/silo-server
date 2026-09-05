import { describe, expect, it, vi, beforeEach } from "vitest";
import type { ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";

import { ApiClientError, type SessionFetchResult } from "@/api/client";
import { V2ProblemError } from "@/api/v2/request";
import deleteSubtitlePreferenceProfileVerificationRequired from "../../../../contracts/api/v2/fixtures/delete_subtitle_preference_profile_verification_required.json";
import updateSubtitlePreferenceValidationFailed from "../../../../contracts/api/v2/fixtures/update_subtitle_preference_validation_failed.json";
import { SETTING_KEYS } from "@/lib/settingsContract";
import { resolveSettingValues, type StoredSettingRow } from "@/lib/settingsResolve";
import { buildSubtitleChoiceRequests } from "@/player/utils/subtitleChoicePersistence";
import type { PlayerSubtitleInfo } from "@/player/types";
import { useDeleteSubtitlePreference, useSetSubtitlePreference } from "./subtitles";

const apiMock = vi.hoisted(() => vi.fn());
const fetchWithSessionMock = vi.hoisted(() => vi.fn());
vi.mock("@/api/client", async () => {
  const actual = await vi.importActual<typeof import("@/api/client")>("@/api/client");
  return { ...actual, api: apiMock, fetchWithSession: fetchWithSessionMock };
});

/** The v2 transport answer for one request, as fetchWithSession hands it to v2(). */
function sessionResponse(res: Response): SessionFetchResult {
  return { res, requestProfileId: null, requestProfileToken: null };
}

/** The v2 requests the hook issued, as `METHOD url` strings. */
function v2Requests(): string[] {
  return fetchWithSessionMock.mock.calls.map(
    ([url, init]) => `${(init as RequestInit).method} ${url as string}`,
  );
}

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
  return buildSubtitleChoiceRequests({ seriesId, index: 0, tracks: TRACKS }).flatMap((request) =>
    request.kind === "setting"
      ? [
          {
            key: decodeURIComponent(request.path.slice("/settings/values/".length).split("?")[0]!),
            scope: "profile_series" as const,
            profileId: "profile-1",
            seriesId,
            value: request.body.value,
          },
        ]
      : [],
  );
}

describe("useDeleteSubtitlePreference", () => {
  beforeEach(() => {
    apiMock.mockReset();
    fetchWithSessionMock
      .mockReset()
      .mockResolvedValue(sessionResponse(new Response(null, { status: 204 })));
  });

  it("clears the canonical profile_series rows alongside the legacy row", async () => {
    // Before this, "Auto" deleted only /subtitle-prefs/{id}. profile_series is
    // the first scope in the resolution order for these keys, so the rows an
    // in-player pick left behind kept resolving the abandoned language for
    // every episode of the series, permanently and unreachably.
    const store = rowsFromInPlayerPick("series-1");
    apiMock.mockImplementation((path: string, options?: RequestInit) => {
      if (options?.method !== "DELETE") return Promise.resolve(undefined);
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
    expect(v2Requests()).toEqual(["DELETE /api/v2/subtitle-prefs/series-1"]);
    expect(apiMock.mock.calls.map(([path]) => path as string)).not.toContain(
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
    apiMock.mockRejectedValue(
      new ApiClientError(404, "not_found", "No value is set at this scope"),
    );

    const { wrapper } = createHarness();
    const { result } = renderHook(() => useDeleteSubtitlePreference(), { wrapper });

    await expect(result.current.mutateAsync("series-1")).resolves.toBeUndefined();
  });

  it("surfaces a real failure rather than reporting a reset that did not happen", async () => {
    apiMock.mockRejectedValue(new ApiClientError(500, "internal_error", "boom"));

    const { wrapper } = createHarness();
    const { result } = renderHook(() => useDeleteSubtitlePreference(), { wrapper });

    await expect(result.current.mutateAsync("series-1")).rejects.toThrow("boom");
  });

  it("surfaces a v2 problem from the preference delete as the contract emits it", async () => {
    // The committed fixture body for this operation; a locked profile must not
    // be reported as a completed reset.
    fetchWithSessionMock.mockResolvedValue(
      sessionResponse(
        new Response(JSON.stringify(deleteSubtitlePreferenceProfileVerificationRequired), {
          status: 403,
          headers: { "Content-Type": "application/problem+json" },
        }),
      ),
    );

    const { wrapper } = createHarness();
    const { result } = renderHook(() => useDeleteSubtitlePreference(), { wrapper });

    const failure = await result.current.mutateAsync("series-1").catch((err: unknown) => err);
    expect(failure).toBeInstanceOf(V2ProblemError);
    expect((failure as V2ProblemError).problemType).toBe("profile_verification_required");
    expect((failure as V2ProblemError).status).toBe(403);
  });
});

describe("useSetSubtitlePreference", () => {
  beforeEach(() => {
    apiMock.mockReset();
    fetchWithSessionMock
      .mockReset()
      .mockResolvedValue(sessionResponse(new Response(null, { status: 204 })));
  });

  /** The JSON body of the single v2 request the hook issued. */
  function v2Body(): unknown {
    const [, init] = fetchWithSessionMock.mock.calls[0] as [string, RequestInit];
    return JSON.parse(init.body as string) as unknown;
  }

  it("replaces the preference through v2 with the chosen track's signature", async () => {
    const { wrapper } = createHarness();
    const { result } = renderHook(() => useSetSubtitlePreference(), { wrapper });

    await result.current.mutateAsync({
      prefId: "series-1",
      selection: {
        source: "embedded",
        language: "ja",
        codec: "subrip",
        label: "Japanese",
        forced: false,
        hearing_impaired: false,
        track_index: 0,
      },
      showForcedSubtitles: true,
    });

    expect(v2Requests()).toEqual(["PUT /api/v2/subtitle-prefs/series-1"]);
    expect(v2Body()).toEqual({
      subtitle_language: "ja",
      subtitle_track_index: 0,
      subtitle_mode: "always",
      track_signature: {
        source: "embedded",
        language: "ja",
        codec: "subrip",
        label: "Japanese",
        forced: false,
        hearing_impaired: false,
      },
      show_forced_subtitles: true,
    });
    expect(apiMock).not.toHaveBeenCalled();
  });

  it('stores "subtitles off" as the -1 sentinel with mode off and no signature', async () => {
    // -1 is the "no track" value every first-party client stores; the v2
    // contract admits it (minimum -1) and spells "no signature" as an absent
    // member rather than null.
    const { wrapper } = createHarness();
    const { result } = renderHook(() => useSetSubtitlePreference(), { wrapper });

    await result.current.mutateAsync({ prefId: "series-1", selection: null });

    expect(v2Requests()).toEqual(["PUT /api/v2/subtitle-prefs/series-1"]);
    expect(v2Body()).toEqual({
      subtitle_language: "",
      subtitle_track_index: -1,
      subtitle_mode: "off",
    });
    expect(apiMock).not.toHaveBeenCalled();
  });

  it("surfaces a v2 validation problem as the contract emits it", async () => {
    fetchWithSessionMock.mockResolvedValue(
      sessionResponse(
        new Response(JSON.stringify(updateSubtitlePreferenceValidationFailed), {
          status: 422,
          headers: { "Content-Type": "application/problem+json" },
        }),
      ),
    );

    const { wrapper } = createHarness();
    const { result } = renderHook(() => useSetSubtitlePreference(), { wrapper });

    const failure = await result.current
      .mutateAsync({ prefId: "series-1", selection: null })
      .catch((err: unknown) => err);
    expect(failure).toBeInstanceOf(V2ProblemError);
    expect((failure as V2ProblemError).problemType).toBe("validation_failed");
    expect((failure as V2ProblemError).status).toBe(422);
  });
});
