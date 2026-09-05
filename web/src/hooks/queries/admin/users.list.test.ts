// @vitest-environment jsdom

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import { createElement } from "react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { installPolicyStorageMocks, jsonResponse } from "@/pages/admin-policy/policyTestUtils";

import { useAdminUsers } from "./users";

// listAdminUsers is cursor paginated on v2 (limit + opaque cursor, page.has_more).
// The admin screens want the full account list, so the hook walks every page.
// This pins the walk: the first request carries no cursor, the next carries
// the cursor the previous page returned, and the walk stops on has_more=false.

function createWrapper() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return function Wrapper({ children }: { children: ReactNode }) {
    return createElement(QueryClientProvider, { client }, children);
  };
}

function v2User(id: string, username: string) {
  return {
    id,
    username,
    email: `${username}@example.test`,
    role: "user",
    permissions: [],
    enabled: true,
    library_ids: null,
    max_playback_quality: null,
    max_streams: null,
    max_transcodes: null,
    transcode_allowed: null,
    audio_transcode_allowed: null,
    max_profiles: 5,
    download_allowed: null,
    download_transcode_allowed: null,
    requests_allowed: null,
    access_group_id: null,
    effective_policy: {
      library_ids: [],
      max_playback_quality: "",
      max_streams: 0,
      max_transcodes: 0,
      transcode_allowed: true,
      audio_transcode_allowed: false,
      download_allowed: true,
      download_transcode_allowed: false,
      requests_allowed: false,
      permissions: [],
    },
    created_at: "2026-01-02T03:04:05.678Z",
    updated_at: "2026-01-02T03:04:05.678Z",
    last_active_at: null,
  };
}

describe("useAdminUsers", () => {
  beforeEach(() => {
    installPolicyStorageMocks();
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("walks every page of the cursor-paginated listing", async () => {
    const requested: URL[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>(async (input) => {
        const url = new URL(String(input), "http://localhost");
        requested.push(url);
        expect(url.pathname).toBe("/api/v2/admin/users");
        if (url.searchParams.get("cursor") === null) {
          return jsonResponse({
            items: [v2User("1", "laura")],
            page: { next_cursor: "cursor-after-1", has_more: true },
          });
        }
        expect(url.searchParams.get("cursor")).toBe("cursor-after-1");
        return jsonResponse({
          items: [v2User("2", "ada")],
          page: { next_cursor: "", has_more: false },
        });
      }),
    );

    const { result } = renderHook(() => useAdminUsers(), { wrapper: createWrapper() });
    await waitFor(() => expect(result.current.isLoading).toBe(false));

    expect(result.current.error).toBeNull();
    expect(requested).toHaveLength(2);
    expect(requested.map((u) => u.searchParams.get("limit"))).toEqual(["200", "200"]);
    expect(result.current.data?.map((u) => [u.id, u.username])).toEqual([
      [1, "laura"],
      [2, "ada"],
    ]);
  });
});
