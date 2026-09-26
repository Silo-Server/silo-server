import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { useSeriesEpisodes } from "@/player/hooks/useSeriesEpisodes";
import { setProfileId } from "@/api/client";
import { installPolicyStorageMocks } from "@/pages/admin-policy/policyTestUtils";
import { useAudiobookGroups } from "./audiobookGroups";
import { useMetadataAIStatus } from "./metadataAI";
import { usePersonSearch } from "./people";
import { personKeys } from "./keys";

describe("catalog query cancellation", () => {
  beforeEach(() => {
    installPolicyStorageMocks();
    setProfileId("p-owner");
  });
  afterEach(() => vi.unstubAllGlobals());

  it.each([
    [
      "audiobook groups",
      () => useAudiobookGroups(3, "author", "name"),
      "/api/v2/catalog/audiobook-groups",
    ],
    [
      "player series seasons",
      () => useSeriesEpisodes("series-1", 1, 3),
      "/api/v2/catalog/series/series-1/seasons",
    ],
    ["people search", () => usePersonSearch("Frank"), "/api/v2/catalog/people"],
    ["metadata AI capability", () => useMetadataAIStatus(), "/api/v2/capabilities/metadata-ai"],
  ] as const)("aborts %s when the observer unmounts", async (_name, useRead, path) => {
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    queryClient.setQueryData(personKeys.searchCapabilities(), { people_media_scope: true });
    let requestSignal: AbortSignal | null | undefined;
    const fetchMock = vi.fn<typeof fetch>(async (_url, options) => {
      requestSignal = options?.signal;
      return new Promise<Response>((_resolve, reject) => {
        requestSignal?.addEventListener(
          "abort",
          () => reject(new DOMException("Aborted", "AbortError")),
          { once: true },
        );
      });
    });
    vi.stubGlobal("fetch", fetchMock);
    const wrapper = ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
    );
    const { unmount } = renderHook(() => useRead(), { wrapper });

    await waitFor(() => expect(requestSignal).toBeDefined());
    expect(String(fetchMock.mock.calls[0]?.[0]).split("?")[0]).toBe(path);
    expect(requestSignal?.aborted).toBe(false);
    unmount();
    expect(requestSignal?.aborted).toBe(true);
    expect(fetchMock).toHaveBeenCalledTimes(1);
    queryClient.clear();
  });

  it("aborts the old search when the normalized search changes", async () => {
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    queryClient.setQueryData(personKeys.searchCapabilities(), { people_media_scope: true });
    const signals: (AbortSignal | null | undefined)[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>(async (_url, options) => {
        const signal = options?.signal;
        signals.push(signal);
        return new Promise<Response>((_resolve, reject) => {
          signal?.addEventListener(
            "abort",
            () => reject(new DOMException("Aborted", "AbortError")),
            { once: true },
          );
        });
      }),
    );
    const wrapper = ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
    );
    const { rerender, unmount } = renderHook(({ search }) => usePersonSearch(search), {
      initialProps: { search: "Frank" },
      wrapper,
    });
    await waitFor(() => expect(signals).toHaveLength(1));
    rerender({ search: "Ursula" });
    await waitFor(() => expect(signals).toHaveLength(2));
    expect(signals[0]?.aborted).toBe(true);
    expect(signals[1]?.aborted).toBe(false);
    unmount();
    expect(signals[1]?.aborted).toBe(true);
    queryClient.clear();
  });
});
