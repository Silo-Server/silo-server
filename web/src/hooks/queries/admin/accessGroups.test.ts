import { setAccessToken, setProfileId, setProfileToken } from "@/api/client";
// @vitest-environment jsdom

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import { createElement } from "react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { captureAccessGroupAuthority } from "@/api/v2/accessGroups";
import type { AccessGroup } from "@/api/types";
import { installPolicyStorageMocks, jsonResponse } from "@/pages/admin-policy/policyTestUtils";

import {
  useAccessGroups,
  useCreateAccessGroup,
  useDeleteAccessGroup,
  useUpdateAccessGroup,
} from "./accessGroups";

const group: AccessGroup = {
  id: 11,
  name: "Household",
  description: "Default household access",
  library_ids: null,
  max_playback_quality: "source",
  download_allowed: true,
  download_transcode_allowed: true,
  transcode_allowed: true,
  audio_transcode_allowed: true,
  max_streams: 0,
  max_transcodes: 0,
  max_remote_stream_bitrate_kbps: 0,
  max_local_stream_bitrate_kbps: 0,
  allowed_permissions: null,
  requests_allowed: true,
  is_default: false,
  member_count: 2,
  created_at: "2026-07-01T12:00:00Z",
  updated_at: "2026-07-01T12:00:00Z",
};

function createWrapper() {
  const client = new QueryClient({
    defaultOptions: {
      queries: { retry: false },
      mutations: { retry: false },
    },
  });
  return function Wrapper({ children }: { children: ReactNode }) {
    return createElement(QueryClientProvider, { client }, children);
  };
}

function requestBody(init: RequestInit | undefined) {
  return JSON.parse(String(init?.body)) as Record<string, unknown>;
}

describe("access group admin hooks", () => {
  beforeEach(() => {
    installPolicyStorageMocks();
    setAccessToken("account");
    setProfileId("owner");
    setProfileToken(null);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("fetches access groups through the shared API client", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>(async (input, init) => {
        expect(String(input)).toBe("/api/v2/admin/access-groups?limit=200");
        expect(init?.method ?? "GET").toBe("GET");
        return jsonResponse({ items: [{ ...group, id: "11" }], page: { has_more: false } });
      }),
    );

    const { result } = renderHook(() => useAccessGroups(), { wrapper: createWrapper() });

    await waitFor(() => expect(result.current.isSuccess).toBe(true));

    expect(result.current.data).toEqual([group]);
  });

  it("posts create requests with nullable access fields", async () => {
    const fetchMock = vi.fn<typeof fetch>(async (input, init) => {
      expect(String(input)).toBe("/api/v2/admin/access-groups");
      expect(init?.method).toBe("POST");
      expect(requestBody(init)).toMatchObject({
        name: "Kids",
        library_ids: null,
        allowed_permissions: null,
      });
      return jsonResponse({ ...group, id: "12", name: "Kids" }, 201);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result } = renderHook(() => useCreateAccessGroup(), { wrapper: createWrapper() });

    await act(async () => {
      await result.current.mutateAsync({
        body: { name: "Kids", library_ids: null, allowed_permissions: null },
        profileContext: captureAccessGroupAuthority(),
      });
    });

    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("puts update requests to the access group detail endpoint", async () => {
    const fetchMock = vi.fn<typeof fetch>(async (input, init) => {
      expect(String(input)).toBe("/api/v2/admin/access-groups/11");
      expect(init?.method).toBe("PUT");
      expect(requestBody(init)).toMatchObject({
        description: "Pinned libraries",
        library_ids: ["1", "2"],
      });
      return new Response(
        JSON.stringify({
          ...group,
          id: "11",
          description: "Pinned libraries",
          library_ids: ["1", "2"],
        }),
        { headers: { "Content-Type": "application/json", ETag: '"saved"' } },
      );
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result } = renderHook(() => useUpdateAccessGroup(), { wrapper: createWrapper() });

    await act(async () => {
      await result.current.mutateAsync({
        editor: { group, etag: '"old"', profileContext: captureAccessGroupAuthority() },
        body: {
          description: "Pinned libraries",
          library_ids: [1, 2],
        },
      });
    });

    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("deletes access groups by id", async () => {
    const fetchMock = vi.fn<typeof fetch>(async (input, init) => {
      expect(String(input)).toBe("/api/v2/admin/access-groups/11");
      expect(init?.method).toBe("DELETE");
      return new Response(null, { status: 204 });
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result } = renderHook(() => useDeleteAccessGroup(), { wrapper: createWrapper() });

    await act(async () => {
      await result.current.mutateAsync({
        group,
        etag: '"old"',
        profileContext: captureAccessGroupAuthority(),
      });
    });

    expect(fetchMock).toHaveBeenCalledTimes(1);
  });
});
