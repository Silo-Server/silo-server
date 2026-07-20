// @vitest-environment jsdom

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import { createElement } from "react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { ebookKeys } from "./keys";
import { useReadingMotivation, useSaveReadingGoals } from "./readingStats";

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function createWrapper(client: QueryClient) {
  return function Wrapper({ children }: { children: ReactNode }) {
    return createElement(QueryClientProvider, { client }, children);
  };
}

function newClient() {
  return new QueryClient({
    defaultOptions: {
      queries: { retry: false },
      mutations: { retry: false },
    },
  });
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("useReadingMotivation", () => {
  it("includes the viewer's timezone in the query key", async () => {
    const mockedZone = "Europe/Amsterdam";
    const resolvedOptionsSpy = vi
      .spyOn(Intl.DateTimeFormat.prototype, "resolvedOptions")
      .mockReturnValue({ timeZone: mockedZone } as Intl.ResolvedDateTimeFormatOptions);

    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>(async () =>
        jsonResponse({
          streak: { current_days: 1, longest_days: 1, today_seconds: 1, today_qualified: true },
          goals: {
            books_per_year: null,
            hours_per_year: null,
            books_finished_ytd: 0,
            hours_ytd: 0,
            books_on_track_for: 0,
            hours_on_track_for: 0,
          },
          challenge: { target_seconds: 0, month_seconds: 0, percent: 0 },
          achievements: [],
          dna: {
            genres: [],
            authors: [],
            diversity_score: 0,
            avg_session_seconds: 0,
            hours_by_bucket: {},
            projected_year_hours: 0,
          },
        }),
      ),
    );

    try {
      const client = newClient();
      const { result } = renderHook(() => useReadingMotivation(), {
        wrapper: createWrapper(client),
      });

      await waitFor(() => expect(result.current.isSuccess).toBe(true));

      expect(client.getQueryData(ebookKeys.readingMotivation(mockedZone))).toBeDefined();
    } finally {
      resolvedOptionsSpy.mockRestore();
    }
  });
});

describe("useSaveReadingGoals", () => {
  it("invalidates the reading-motivation query on a successful save", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>(async (input, init) => {
        expect(String(input)).toBe("/api/v1/ebooks/reading-goals");
        expect(init?.method).toBe("PUT");
        return new Response(null, { status: 204 });
      }),
    );

    const client = newClient();
    const invalidateSpy = vi.spyOn(client, "invalidateQueries");
    const { result } = renderHook(() => useSaveReadingGoals(), {
      wrapper: createWrapper(client),
    });

    await act(async () => {
      await result.current.mutateAsync({ books_per_year: 30, hours_per_year: 100 });
    });

    expect(invalidateSpy).toHaveBeenCalledWith({ queryKey: ebookKeys.readingMotivation() });
  });

  it("rejects mutateAsync when the save fails, without invalidating", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>(async () =>
        jsonResponse({ error: "server_error", message: "Nope" }, 500),
      ),
    );

    const client = newClient();
    const invalidateSpy = vi.spyOn(client, "invalidateQueries");
    const { result } = renderHook(() => useSaveReadingGoals(), {
      wrapper: createWrapper(client),
    });

    await expect(
      act(async () => {
        await result.current.mutateAsync({ books_per_year: 30, hours_per_year: 100 });
      }),
    ).rejects.toThrow();

    expect(invalidateSpy).not.toHaveBeenCalled();
  });
});
