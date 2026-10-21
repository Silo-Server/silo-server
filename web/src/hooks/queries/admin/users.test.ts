import { setAccessToken, setProfileId, setProfileToken } from "@/api/client";
// @vitest-environment jsdom

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import { createElement } from "react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { installPolicyStorageMocks, jsonResponse } from "@/pages/admin-policy/policyTestUtils";

import { useAdminUser, useAdminUsers } from "./users";

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

function userResponse(etag?: string) {
  return new Response(JSON.stringify(v2User("7", "laura")), {
    headers: { "Content-Type": "application/json", ...(etag ? { ETag: etag } : {}) },
  });
}

describe("useAdminUser", () => {
  beforeEach(() => {
    installPolicyStorageMocks();
    setAccessToken("account");
    setProfileId("owner");
    setProfileToken(null);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("offers an editor when the account arrives with a strong ETag", async () => {
    vi.stubGlobal("fetch", vi.fn<typeof fetch>().mockResolvedValue(userResponse('"rev-1"')));
    const { result } = renderHook(() => useAdminUser(7), { wrapper: createWrapper() });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data?.username).toBe("laura");
    expect(result.current.editor?.etag).toBe('"rev-1"');
  });

  it("shows the account read-only when a proxy strips the ETag", async () => {
    vi.stubGlobal("fetch", vi.fn<typeof fetch>().mockResolvedValue(userResponse()));
    const { result } = renderHook(() => useAdminUser(7), { wrapper: createWrapper() });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data?.username).toBe("laura");
    expect(result.current.editor).toBeUndefined();
  });
});

// listAdminUsers is cursor paginated on v2 (limit + opaque cursor, page.has_more).
// The admin screens want the full account list, so the hook walks every page.
// This pins the walk: the first request carries no cursor, the next carries
// the cursor the previous page returned, and the walk stops on has_more=false.
describe("useAdminUsers", () => {
  beforeEach(() => {
    setAccessToken("account");
    setProfileId("owner");
    setProfileToken(null);
    installPolicyStorageMocks();
    setAccessToken("account");
    setProfileId("owner");
    setProfileToken(null);
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
