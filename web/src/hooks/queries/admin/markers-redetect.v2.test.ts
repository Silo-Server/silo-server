// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import { createElement, type ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { installPolicyStorageMocks, jsonResponse } from "@/pages/admin-policy/policyTestUtils";
import { useRedetectEpisodeIntro, useRedetectItemMarkers } from "../items";
import { setAccessToken, setProfileId, setRefreshToken } from "@/api/client";
import { useAdminMarkerCapabilities, useMarkerDetectionKinds } from "./markers";
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));
beforeEach(installPolicyStorageMocks);
afterEach(() => vi.unstubAllGlobals());
function setup(response: (path: string) => Response) {
  const calls: string[] = [];
  const bodies: unknown[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = new URL(String(input), "http://localhost").pathname;
      calls.push(path);
      bodies.push(typeof init?.body === "string" ? JSON.parse(init.body) : init?.body);
      return response(path);
    }),
  );
  const client = new QueryClient({ defaultOptions: { mutations: { retry: 3 } } });
  return {
    calls,
    bodies,
    client,
    wrapper: ({ children }: { children: ReactNode }) =>
      createElement(QueryClientProvider, { client }, children),
  };
}
it("does not refresh or replay re-detection after401", async () => {
  setAccessToken("synthetic-admin");
  setRefreshToken("synthetic-refresh");
  const { calls, wrapper } = setup(
    () =>
      new Response(
        JSON.stringify({
          type: "https://siloserver.org/docs/api/v2/problems/invalid_token",
          title: "Invalid token",
          status: 401,
          detail: "Expired",
          instance: "urn:silo:request:test",
        }),
        { status: 401, headers: { "Content-Type": "application/problem+json" } },
      ),
  );
  const { result } = renderHook(useRedetectItemMarkers, { wrapper });
  await act(async () => {
    await expect(
      result.current.mutateAsync({ itemId: "episode-1", kind: "all" }),
    ).rejects.toThrow();
  });
  expect(calls).toEqual(["/api/v2/admin/items/episode-1/redetect-markers"]);
});
it.each(["queued", "already_running"] as const)("preserves %s acknowledgment", async (status) => {
  const { calls, wrapper } = setup(() => jsonResponse({ status }, 202));
  const { result } = renderHook(useRedetectItemMarkers, { wrapper });
  await act(async () => {
    await expect(
      result.current.mutateAsync({ itemId: "episode-1", kind: "intro" }),
    ).resolves.toEqual({ status });
  });
  expect(calls).toHaveLength(1);
});
it.each(["intro", "credits", "all"] as const)("sends the %s kind", async (kind) => {
  const { calls, bodies, wrapper } = setup(() => jsonResponse({ status: "queued" }, 202));
  const { result } = renderHook(useRedetectItemMarkers, { wrapper });
  await act(async () => {
    await result.current.mutateAsync({ itemId: "movie-1", kind });
  });
  expect(calls).toEqual(["/api/v2/admin/items/movie-1/redetect-markers"]);
  expect(bodies).toEqual([{ kind }]);
});
it("falls back to the episode-only intro operation", async () => {
  const { calls, bodies, wrapper } = setup(() => jsonResponse({ status: "queued" }, 202));
  const { result } = renderHook(useRedetectEpisodeIntro, { wrapper });
  await act(async () => {
    await result.current.mutateAsync("episode-1");
  });
  expect(calls).toEqual(["/api/v2/admin/items/episode-1/redetect-intro"]);
  expect(bodies).toEqual([undefined]);
});
it("reads the marker capability document once, without retrying a failure", async () => {
  const { calls, wrapper } = setup(() => jsonResponse({ title: "Not Found", status: 404 }, 404));
  const { result } = renderHook(() => useAdminMarkerCapabilities(), { wrapper });
  await waitFor(() => expect(result.current.isError).toBe(true));
  expect(result.current.data?.redetect_markers).toBeUndefined();
  expect(calls).toEqual(["/api/v2/admin/markers/capabilities"]);
});
it("does not read marker capabilities for non-admins", () => {
  const { calls, wrapper } = setup(() => jsonResponse({}, 200));
  renderHook(() => useAdminMarkerCapabilities(false), { wrapper });
  expect(calls).toEqual([]);
});

describe("useMarkerDetectionKinds", () => {
  beforeEach(() => {
    setAccessToken("synthetic-admin");
    setProfileId("admin-profile");
  });

  function detectionServer(detectionKindSettings: boolean, settings: Record<string, string>) {
    return (path: string) =>
      path.endsWith("/markers/capabilities")
        ? jsonResponse({ redetect_markers: true, detection_kind_settings: detectionKindSettings })
        : jsonResponse(settings);
  }

  it.each([
    [{}, { intro: true, credits: true }],
    [{ "markers.detect_credits": "false" }, { intro: true, credits: false }],
    [
      { "markers.detect_intros": " FALSE ", "markers.detect_credits": "" },
      { intro: false, credits: true },
    ],
  ])("reads %j as %j", async (settings, kinds) => {
    const { calls, wrapper } = setup(detectionServer(true, settings));
    const { result } = renderHook(() => useMarkerDetectionKinds(), { wrapper });
    await waitFor(() => expect(result.current).toEqual(kinds));
    expect(calls).toEqual([
      "/api/v2/admin/markers/capabilities",
      "/api/v2/admin/settings/effective",
    ]);
  });

  it("offers every kind on a node that ignores the settings", async () => {
    const { calls, wrapper } = setup(detectionServer(false, { "markers.detect_credits": "false" }));
    const { result } = renderHook(() => useMarkerDetectionKinds(), { wrapper });
    await waitFor(() => expect(calls).toEqual(["/api/v2/admin/markers/capabilities"]));
    expect(result.current).toBeUndefined();
  });

  it.each(["capabilities", "settings"])(
    "offers every kind again when the %s refetch fails",
    async (failing) => {
      let fail = false;
      const { client, wrapper } = setup((path) => {
        const isFailing = path.endsWith(
          failing === "capabilities" ? "/markers/capabilities" : "/settings/effective",
        );
        if (fail && isFailing) return jsonResponse({ title: "Unavailable", status: 503 }, 503);
        return detectionServer(true, { "markers.detect_credits": "false" })(path);
      });
      client.setDefaultOptions({ queries: { retry: false } });
      const { result } = renderHook(() => useMarkerDetectionKinds(), { wrapper });
      await waitFor(() => expect(result.current).toEqual({ intro: true, credits: false }));

      fail = true;
      await act(() => client.refetchQueries());
      await waitFor(() => expect(result.current).toBeUndefined());
    },
  );

  it("reads nothing until enabled", () => {
    const { calls, wrapper } = setup(detectionServer(true, {}));
    renderHook(() => useMarkerDetectionKinds(false), { wrapper });
    expect(calls).toEqual([]);
  });
});

// An older API node answers the route it does not have with a 404.
function missingMarkersRoute(path: string) {
  return path.endsWith("/redetect-markers")
    ? jsonResponse({ title: "Not Found", status: 404 }, 404)
    : jsonResponse({ status: "queued" }, 202);
}

it.each(["intro"] as const)(
  "retries an episode %s request through redetect-intro when redetect-markers is missing",
  async (kind) => {
    const { calls, bodies, client, wrapper } = setup(missingMarkersRoute);
    const invalidate = vi.spyOn(client, "invalidateQueries");
    const { result } = renderHook(() => useRedetectItemMarkers({ introFallback: true }), {
      wrapper,
    });
    await act(async () => {
      await expect(result.current.mutateAsync({ itemId: "episode-1", kind })).resolves.toEqual({
        status: "queued",
      });
    });
    expect(calls).toEqual([
      "/api/v2/admin/items/episode-1/redetect-markers",
      "/api/v2/admin/items/episode-1/redetect-intro",
    ]);
    expect(bodies).toEqual([{ kind }, undefined]);
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ["admin", "markerCapabilities"] });
  },
);

it.each([
  ["an episode credits request", { introFallback: true }, "credits"],
  ["an episode all request", { introFallback: true }, "all"],
  ["a movie credits request", {}, "credits"],
  ["a movie all request", {}, "all"],
] as const)("surfaces the 404 for %s", async (_label, options, kind) => {
  const { calls, client, wrapper } = setup(missingMarkersRoute);
  const invalidate = vi.spyOn(client, "invalidateQueries");
  const { result } = renderHook(() => useRedetectItemMarkers(options), { wrapper });
  await act(async () => {
    await expect(result.current.mutateAsync({ itemId: "item-1", kind })).rejects.toThrow();
  });
  expect(calls).toEqual(["/api/v2/admin/items/item-1/redetect-markers"]);
  expect(invalidate).toHaveBeenCalledWith({ queryKey: ["admin", "markerCapabilities"] });
});

it("does not fall back or drop capabilities on other failures", async () => {
  const { calls, client, wrapper } = setup(() =>
    jsonResponse({ title: "Conflict", status: 409 }, 409),
  );
  const invalidate = vi.spyOn(client, "invalidateQueries");
  const { result } = renderHook(() => useRedetectItemMarkers({ introFallback: true }), {
    wrapper,
  });
  await act(async () => {
    await expect(
      result.current.mutateAsync({ itemId: "episode-1", kind: "intro" }),
    ).rejects.toThrow();
  });
  expect(calls).toEqual(["/api/v2/admin/items/episode-1/redetect-markers"]);
  expect(invalidate).not.toHaveBeenCalled();
});
