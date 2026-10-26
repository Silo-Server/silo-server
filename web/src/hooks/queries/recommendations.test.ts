// @vitest-environment jsdom

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook } from "@testing-library/react";
import { createElement } from "react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import createTasteSeedOk from "../../../../contracts/api/v2/fixtures/create_taste_seed_ok.json";

import { setProfileId } from "@/api/client";
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

  it("refetches the recommendation reads again after a submission", async () => {
    stubFetch(() => jsonResponse(createTasteSeedOk));
    const { result, recInvalidations, homeBumps } = renderSubmit();

    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    try {
      await result.current.mutateAsync(["movie:heat-1995"]);
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
      await result.current.mutateAsync(["movie:heat-1995"]);
      await vi.advanceTimersByTimeAsync(1_000);
      await result.current.mutateAsync(["movie:alien-1979"]);
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
