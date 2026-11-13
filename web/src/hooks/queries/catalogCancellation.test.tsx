import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { setProfileId } from "@/api/client";
import { installPolicyStorageMocks } from "@/pages/admin-policy/policyTestUtils";
import { usePersonSearch } from "./personSearch";
import { personKeys } from "./keys";

describe("catalog query cancellation", () => {
  beforeEach(() => {
    installPolicyStorageMocks();
    setProfileId("p-owner");
  });
  afterEach(() => vi.unstubAllGlobals());

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
