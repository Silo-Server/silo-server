// @vitest-environment jsdom

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook } from "@testing-library/react";
import { createElement } from "react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import createTasteSeedOk from "../../../../contracts/api/v2/fixtures/create_taste_seed_ok.json";

import { setAccessToken, setProfileId } from "@/api/client";
import { mediaSurfaceKeys } from "./keys";
import { installPolicyStorageMocks, jsonResponse } from "@/pages/admin-policy/policyTestUtils";
import { useSubmitTasteSeed } from "./tasteSeed";

type FetchMock = ReturnType<typeof vi.fn<typeof fetch>>;

function stubFetch(handler: (url: URL, init?: RequestInit) => Response): FetchMock {
  const fetchMock = vi.fn<typeof fetch>(async (input, init) =>
    handler(new URL(String(input), "http://localhost"), init),
  );
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

describe("taste seeding on the v2 contract", () => {
  beforeEach(() => {
    installPolicyStorageMocks();
    setProfileId("p-owner");
  });

  afterEach(() => {
    setAccessToken(null);
    vi.unstubAllGlobals();
  });

  // The server answers once the profile's refresh finished or after a few
  // seconds; the recommendation reads are refetched again later so a slower
  // refresh still shows up. Home, which reloads every section on a bump, is
  // bumped on success and once more with the last refetch.
  function renderSubmit() {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const invalidate = vi.spyOn(client, "invalidateQueries");
    const recInvalidations = () =>
      invalidate.mock.calls.filter(([filters]) => filters?.queryKey?.[0] === "recommendations")
        .length;
    const setData = vi.spyOn(client, "setQueryData");
    const signalKey = JSON.stringify(mediaSurfaceKeys.refreshSignal());
    const homeBumps = () =>
      setData.mock.calls.filter(([key]) => JSON.stringify(key) === signalKey).length;
    const { result } = renderHook(() => useSubmitTasteSeed(), {
      wrapper: ({ children }: { children: ReactNode }) =>
        createElement(QueryClientProvider, { client }, children),
    });
    return { result, recInvalidations, homeBumps };
  }

  // Unselecting a favourite removes it. The batch goes first, since the
  // server waits for its refresh only when none is already queued, and a
  // save that only removes skips the batch, whose endpoint needs a title.
  it("adds the new picks, then removes unselected favourites", async () => {
    const requests: string[] = [];
    stubFetch((url, init) => {
      requests.push(`${init?.method ?? "GET"} ${decodeURIComponent(url.pathname)}`);
      return init?.method === "DELETE"
        ? new Response(null, { status: 204 })
        : jsonResponse(createTasteSeedOk);
    });
    const { result } = renderSubmit();

    await expect(
      result.current.mutateAsync({ add: ["movie:heat-1995"], remove: ["movie:alien-1979"] }),
    ).resolves.toEqual({ added: createTasteSeedOk.added, removed: 1 });
    expect(requests).toEqual([
      "POST /api/v2/recommendations/taste-seed",
      "DELETE /api/v2/favorites/movie:alien-1979",
    ]);

    requests.length = 0;
    await expect(
      result.current.mutateAsync({ add: [], remove: ["movie:alien-1979"] }),
    ).resolves.toEqual({ added: 0, removed: 1 });
    expect(requests).toEqual(["DELETE /api/v2/favorites/movie:alien-1979"]);
  });

  // A failed removal fails the save, so the picker stays open and the user
  // can send it again; the removals after it are not sent.
  it("fails the save when a removal fails", async () => {
    const requests: string[] = [];
    stubFetch((url, init) => {
      requests.push(`${init?.method ?? "GET"} ${decodeURIComponent(url.pathname)}`);
      return init?.method === "DELETE"
        ? jsonResponse({ title: "Internal Server Error", status: 500 }, 500)
        : jsonResponse(createTasteSeedOk);
    });
    const { result } = renderSubmit();

    await expect(
      result.current.mutateAsync({
        add: ["movie:heat-1995"],
        remove: ["movie:alien-1979", "movie:ran-1985"],
      }),
    ).rejects.toThrow();
    expect(requests).toEqual([
      "POST /api/v2/recommendations/taste-seed",
      "DELETE /api/v2/favorites/movie:alien-1979",
    ]);
  });

  // A profile switch partway through a save must not send the rest of it as
  // the newly active profile.
  it("sends the whole save as the profile that started it", async () => {
    setAccessToken("token-owner");
    const profiles: (string | null)[] = [];
    stubFetch((_url, init) => {
      profiles.push(new Headers(init?.headers).get("X-Profile-Id"));
      if (init?.method === "DELETE") return new Response(null, { status: 204 });
      setProfileId("p-kid");
      return jsonResponse(createTasteSeedOk);
    });
    const { result } = renderSubmit();

    await result.current.mutateAsync({ add: ["movie:heat-1995"], remove: ["movie:alien-1979"] });
    expect(profiles).toEqual(["p-owner", "p-owner"]);
  });

  it("refetches the recommendation reads again after a submission", async () => {
    stubFetch(() => jsonResponse(createTasteSeedOk));
    const { result, recInvalidations, homeBumps } = renderSubmit();

    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    try {
      await result.current.mutateAsync({ add: ["movie:heat-1995"], remove: [] });
      expect(recInvalidations()).toBe(1);
      expect(homeBumps()).toBe(1);

      await vi.advanceTimersByTimeAsync(3_000);
      expect(recInvalidations()).toBe(2);
      await vi.advanceTimersByTimeAsync(12_000);
      expect(recInvalidations()).toBe(3);
      await vi.advanceTimersByTimeAsync(30_000);
      expect(recInvalidations()).toBe(4);
      await vi.advanceTimersByTimeAsync(80_000);
      expect(recInvalidations()).toBe(5);
      expect(homeBumps()).toBe(1);
      // The last refetch comes after two of the server's two-minute refresh
      // timeouts: a refresh and the rerun the submission asked for.
      await vi.advanceTimersByTimeAsync(120_000);
      expect(recInvalidations()).toBe(6);
      expect(homeBumps()).toBe(2);
      await vi.advanceTimersByTimeAsync(300_000);
      expect(recInvalidations()).toBe(6);
    } finally {
      vi.useRealTimers();
    }
  });

  // A second submission replaces the first one's refetches rather than
  // adding its own, and a profile switch cancels what is left.
  it("keeps one refetch schedule and drops it on a profile switch", async () => {
    stubFetch(() => jsonResponse(createTasteSeedOk));
    const { result, recInvalidations, homeBumps } = renderSubmit();

    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    try {
      await result.current.mutateAsync({ add: ["movie:heat-1995"], remove: [] });
      await vi.advanceTimersByTimeAsync(1_000);
      await result.current.mutateAsync({ add: ["movie:alien-1979"], remove: [] });
      expect(recInvalidations()).toBe(2);

      await vi.advanceTimersByTimeAsync(3_000);
      expect(recInvalidations()).toBe(3);

      setProfileId("p-kid");
      await vi.advanceTimersByTimeAsync(300_000);
      expect(recInvalidations()).toBe(3);
      expect(homeBumps()).toBe(2);
    } finally {
      vi.useRealTimers();
    }
  });
});
