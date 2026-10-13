// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import { createElement } from "react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { setAccessToken, setProfileId, setProfileToken } from "@/api/client";
import { sectionKeys } from "@/hooks/queries/keys";
import { installPolicyStorageMocks, jsonResponse } from "@/pages/admin-policy/policyTestUtils";

import {
  useAdminProfileSectionOverrides,
  useAdminProfileSectionSettings,
  useResetAdminProfileSections,
  useSaveAdminProfileSections,
} from "./profileSections";

const BASE = "/api/v2/admin/users/7/profiles/p%202/sections";

function createWrapper() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return function Wrapper({ children }: { children: ReactNode }) {
    return createElement(QueryClientProvider, { client }, children);
  };
}

describe("admin profile section hooks", () => {
  beforeEach(() => {
    installPolicyStorageMocks();
    setAccessToken("account");
    setProfileId("owner");
    setProfileToken(null);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("reads one profile's Home layout and saved overrides from the admin routes", async () => {
    const urls: string[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>(async (input) => {
        const url = String(input);
        urls.push(url);
        if (url === `${BASE}/settings?scope=home`) {
          return jsonResponse({
            items: [
              {
                id: "s-continue",
                section_type: "continue_watching",
                title: "Continue Watching",
                featured: false,
                item_limit: 20,
                hidden: true,
                is_custom: false,
                customized: true,
                position: 0,
              },
            ],
          });
        }
        return jsonResponse({
          items: [
            {
              id: "o-1",
              section_id: "s-continue",
              position: null,
              hidden: true,
              removed: false,
              section_type: "",
              title: "",
              featured: null,
              item_limit: null,
              is_user_added: false,
              user_section_type: "",
              user_title: "",
              created_at: null,
              updated_at: null,
            },
          ],
        });
      }),
    );

    const wrapper = createWrapper();
    const settings = renderHook(() => useAdminProfileSectionSettings(7, "p 2"), { wrapper });
    const overrides = renderHook(() => useAdminProfileSectionOverrides(7, "p 2"), { wrapper });
    await waitFor(() => expect(settings.result.current.isSuccess).toBe(true));
    await waitFor(() => expect(overrides.result.current.isSuccess).toBe(true));

    expect(settings.result.current.data?.map((s) => s.id)).toEqual(["s-continue"]);
    // Null members become absent ones, as on the profile's own route.
    expect(overrides.result.current.data).toEqual([
      expect.objectContaining({ id: "o-1", section_id: "s-continue", position: undefined }),
    ]);
    expect(urls.sort()).toEqual([`${BASE}/settings?scope=home`, `${BASE}?scope=home`]);
  });

  it("replaces and resets only the addressed profile's Home overrides", async () => {
    const requests: { url: string; method?: string; body?: unknown }[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>(async (input, init) => {
        requests.push({
          url: String(input),
          method: init?.method,
          body: init?.body ? JSON.parse(String(init.body)) : undefined,
        });
        return new Response(null, { status: 204 });
      }),
    );

    const wrapper = createWrapper();
    const save = renderHook(() => useSaveAdminProfileSections(7, "p 2"), { wrapper });
    const reset = renderHook(() => useResetAdminProfileSections(7, "p 2"), { wrapper });
    await act(() =>
      save.result.current.mutateAsync([{ id: "o-1", section_id: "s-continue", position: 0 }]),
    );
    await act(() => reset.result.current.mutateAsync());

    expect(requests).toEqual([
      {
        url: `${BASE}?scope=home`,
        method: "PUT",
        body: { overrides: [{ id: "o-1", section_id: "s-continue", position: 0 }] },
      },
      { url: `${BASE}?scope=home`, method: "DELETE", body: undefined },
    ]);
  });

  it("marks the acting profile's own section caches stale after save and reset", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>(async () => new Response(null, { status: 204 })),
    );
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
    });
    const wrapper = ({ children }: { children: ReactNode }) =>
      createElement(QueryClientProvider, { client }, children);
    const seed = () => {
      client.setQueryData(sectionKeys.homeLayout(), []);
      client.setQueryData(sectionKeys.profileOverridesRaw("home"), []);
    };
    const stale = () =>
      [sectionKeys.homeLayout(), sectionKeys.profileOverridesRaw("home")].map(
        (queryKey) => client.getQueryState(queryKey)?.isInvalidated,
      );

    const save = renderHook(() => useSaveAdminProfileSections(7, "p 2"), { wrapper });
    seed();
    await act(() => save.result.current.mutateAsync([]));
    expect(stale()).toEqual([true, true]);

    const reset = renderHook(() => useResetAdminProfileSections(7, "p 2"), { wrapper });
    seed();
    await act(() => reset.result.current.mutateAsync());
    expect(stale()).toEqual([true, true]);
  });

  it("renders the save and reset hooks without a profile context, refusing only when called", async () => {
    const fetchMock = vi.fn<typeof fetch>(async () => new Response(null, { status: 204 }));
    vi.stubGlobal("fetch", fetchMock);
    setProfileId(null);

    const wrapper = createWrapper();
    const save = renderHook(() => useSaveAdminProfileSections(7, "p 2"), { wrapper });
    const reset = renderHook(() => useResetAdminProfileSections(7, "p 2"), { wrapper });

    await expect(act(() => save.result.current.mutateAsync([]))).rejects.toThrow();
    await expect(act(() => reset.result.current.mutateAsync())).rejects.toThrow();
    expect(fetchMock).not.toHaveBeenCalled();
  });
});
