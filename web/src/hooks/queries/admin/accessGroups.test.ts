// @vitest-environment jsdom

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import { createElement } from "react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

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
  max_streams: 0,
  max_transcodes: 0,
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
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("fetches access groups through the shared API client", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>(async (input, init) => {
        expect(String(input)).toBe("/api/v1/admin/access-groups");
        expect(init?.method ?? "GET").toBe("GET");
        return jsonResponse([group]);
      }),
    );

    const { result } = renderHook(() => useAccessGroups(), { wrapper: createWrapper() });

    await waitFor(() => expect(result.current.isSuccess).toBe(true));

    expect(result.current.data).toEqual([group]);
  });

  it("posts create requests with nullable access fields", async () => {
    const fetchMock = vi.fn<typeof fetch>(async (input, init) => {
      expect(String(input)).toBe("/api/v1/admin/access-groups");
      expect(init?.method).toBe("POST");
      expect(requestBody(init)).toMatchObject({
        name: "Kids",
        library_ids: null,
        allowed_permissions: null,
      });
      return jsonResponse({ ...group, id: 12, name: "Kids" }, 201);
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result } = renderHook(() => useCreateAccessGroup(), { wrapper: createWrapper() });

    await act(async () => {
      await result.current.mutateAsync({
        name: "Kids",
        library_ids: null,
        allowed_permissions: null,
      });
    });

    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("puts update requests to the access group detail endpoint", async () => {
    const fetchMock = vi.fn<typeof fetch>(async (input, init) => {
      expect(String(input)).toBe("/api/v1/admin/access-groups/11");
      expect(init?.method).toBe("PUT");
      expect(requestBody(init)).toMatchObject({
        description: "Pinned libraries",
        library_ids: [1, 2],
      });
      return jsonResponse({ ...group, description: "Pinned libraries", library_ids: [1, 2] });
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result } = renderHook(() => useUpdateAccessGroup(), { wrapper: createWrapper() });

    await act(async () => {
      await result.current.mutateAsync({
        id: 11,
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
      expect(String(input)).toBe("/api/v1/admin/access-groups/11");
      expect(init?.method).toBe("DELETE");
      return new Response(null, { status: 204 });
    });
    vi.stubGlobal("fetch", fetchMock);

    const { result } = renderHook(() => useDeleteAccessGroup(), { wrapper: createWrapper() });

    await act(async () => {
      await result.current.mutateAsync(11);
    });

    expect(fetchMock).toHaveBeenCalledTimes(1);
  });
});
