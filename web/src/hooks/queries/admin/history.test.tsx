// @vitest-environment jsdom

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { createElement, type ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  setAccessToken,
  setProfileId,
  setProfileToken,
  StaleApiRequestContextError,
} from "@/api/client";
import { V2ProblemError } from "@/api/v2/request";
import {
  adminPlaybackHistoryItemFromV2,
  adminPlaybackHistoryScope,
  listAdminPlaybackHistory,
} from "@/api/v2/adminPlaybackHistory";
import { installPolicyStorageMocks, jsonResponse } from "@/pages/admin-policy/policyTestUtils";
import { buildAdminPlaybackHistoryQuery, useAdminPlaybackHistory } from "./history";

const entry = {
  session_id: "sess-1",
  user_id: "7",
  username: "laura",
  profile_id: "p-owner",
  profile_name: "Laura",
  media_item_id: "movie-1",
  media_file_id: "42",
  media_title: "Synthetic Movie",
  media_type: "movie",
  play_method: "direct_play",
  started_at: "2026-01-02T02:04:05.678Z",
  ended_at: "2026-01-02T03:04:05.678Z",
  watched_seconds: 3600,
  duration_seconds: null,
  completed: true,
};

function page(items: unknown[], next?: string) {
  return { items, page: { has_more: Boolean(next), ...(next ? { next_cursor: next } : {}) } };
}

function wrapper() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return ({ children }: { children: ReactNode }) =>
    createElement(QueryClientProvider, { client }, children);
}

type FetchMock = ReturnType<typeof vi.fn<typeof fetch>>;
function requestOf(fetchMock: FetchMock, index = 0) {
  const [input, init] = fetchMock.mock.calls[index]!;
  return { url: String(input), headers: new Headers(init?.headers) };
}

beforeEach(() => {
  installPolicyStorageMocks();
  setAccessToken("synthetic-access");
  setProfileId("p-primary");
  setProfileToken("pin-proof");
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.clearAllMocks();
});

describe("buildAdminPlaybackHistoryQuery", () => {
  it("serializes every filter and omits the completion filter for all", () => {
    expect(
      buildAdminPlaybackHistoryQuery({
        userId: 7,
        profileId: "prof-1",
        mediaItemId: "movie-100",
        completed: "false",
        limit: 50,
      }),
    ).toEqual({
      limit: 50,
      user_id: "7",
      profile_id: "prof-1",
      media_item_id: "movie-100",
      completed: "false",
    });
    expect(buildAdminPlaybackHistoryQuery({ completed: "all" })).toEqual({ limit: 200 });
    expect(buildAdminPlaybackHistoryQuery({ limit: 1000 })).toEqual({ limit: 200 });
  });
});

describe("listAdminPlaybackHistory", () => {
  it("sends the captured authority and projects string IDs onto the view rows", async () => {
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(jsonResponse(page([entry])));
    vi.stubGlobal("fetch", fetchMock);
    const result = await listAdminPlaybackHistory(
      { limit: 100, user_id: "7", completed: "true" },
      adminPlaybackHistoryScope(),
    );
    const { url, headers } = requestOf(fetchMock);
    expect(url).toBe("/api/v2/admin/playback-history?limit=100&user_id=7&completed=true");
    expect(headers.get("Authorization")).toBe("Bearer synthetic-access");
    expect(headers.get("X-Profile-Id")).toBe("p-primary");
    expect(headers.get("X-Profile-Token")).toBe("pin-proof");
    expect(result).toEqual({
      items: [adminPlaybackHistoryItemFromV2(entry as never)],
      hasMore: false,
      nextCursor: null,
    });
    expect(result.items[0]).toMatchObject({
      user_id: 7,
      media_file_id: 42,
      duration_seconds: null,
    });
  });

  it("refuses a page whose authority changed while it was in flight", async () => {
    const scope = adminPlaybackHistoryScope();
    const fetchMock = vi.fn<typeof fetch>().mockImplementation(async () => {
      setProfileId("p-other");
      return jsonResponse(page([entry]));
    });
    vi.stubGlobal("fetch", fetchMock);
    await expect(listAdminPlaybackHistory({ limit: 1 }, scope)).rejects.toBeInstanceOf(
      StaleApiRequestContextError,
    );
    expect(adminPlaybackHistoryScope()).not.toBe(scope);
  });

  it("refuses dispatch without a captured profile and a stale scope without a request", async () => {
    const fetchMock = vi.fn<typeof fetch>();
    vi.stubGlobal("fetch", fetchMock);
    const scope = adminPlaybackHistoryScope();
    await expect(
      listAdminPlaybackHistory({ limit: 1 }, "playback-history:0"),
    ).rejects.toBeInstanceOf(StaleApiRequestContextError);
    setProfileId(null);
    await expect(listAdminPlaybackHistory({ limit: 1 }, scope)).rejects.toBeInstanceOf(
      StaleApiRequestContextError,
    );
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("rejects malformed pages and surfaces problem documents", async () => {
    const scope = adminPlaybackHistoryScope();
    for (const body of [
      page([{ ...entry, user_id: "0" }]),
      page([entry], "cursor-1") && { items: [entry], page: { has_more: true } },
      { items: [], page: { has_more: false, next_cursor: "leftover" } },
      { items: [entry] },
    ]) {
      vi.stubGlobal("fetch", vi.fn<typeof fetch>().mockResolvedValue(jsonResponse(body)));
      await expect(listAdminPlaybackHistory({ limit: 1 }, scope)).rejects.toThrow(
        "Invalid playback history page",
      );
    }
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>().mockResolvedValue(
        jsonResponse(
          {
            type: "https://siloserver.org/docs/api/v2/problems/permission_denied",
            title: "Permission denied",
            status: 403,
            detail: "Admin access required.",
          },
          403,
        ),
      ),
    );
    await expect(listAdminPlaybackHistory({ limit: 1 }, scope)).rejects.toBeInstanceOf(
      V2ProblemError,
    );
  });
});

describe("useAdminPlaybackHistory", () => {
  it("reads the first page through v2 and keys the cache by authority generation", async () => {
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(jsonResponse(page([entry], "c-2")));
    vi.stubGlobal("fetch", fetchMock);
    const { result } = renderHook(
      () => useAdminPlaybackHistory({ userId: 7, completed: "all", limit: 50 }),
      { wrapper: wrapper() },
    );
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toHaveLength(1);
    expect(result.current.data?.[0]?.session_id).toBe("sess-1");
    expect(requestOf(fetchMock).url).toBe("/api/v2/admin/playback-history?limit=50&user_id=7");
  });

  it("does not deliver a page fetched under a replaced profile", async () => {
    const releases: Array<(value: Response) => void> = [];
    const fetchMock = vi
      .fn<typeof fetch>()
      .mockImplementation(() => new Promise<Response>((resolve) => releases.push(resolve)));
    vi.stubGlobal("fetch", fetchMock);
    const { result, rerender } = renderHook(() => useAdminPlaybackHistory({ limit: 25 }), {
      wrapper: wrapper(),
    });
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
    act(() => setProfileId("p-other"));
    rerender();
    // The hook re-keys under the new authority and starts its own request; the
    // first page is released afterwards and must be refused, not cached.
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    expect(requestOf(fetchMock, 1).headers.get("X-Profile-Id")).toBe("p-other");
    releases[0]!(jsonResponse(page([entry])));
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(result.current.data).toBeUndefined();
    expect(result.current.isPending).toBe(true);
  });
});
