import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { HomeSectionItemsResponse } from "@/api/types";
import { sectionKeys } from "./keys";

const mocks = vi.hoisted(() => ({
  activeProfile: "p-owner",
  fetchWithSession: vi.fn(),
  toastError: vi.fn(),
  toastSuccess: vi.fn(),
}));

vi.mock("@/api/client", () => ({
  fetchWithSession: (...args: unknown[]) => mocks.fetchWithSession(...args),
  reportProfileUnverified: vi.fn(),
  captureProfileRequestContext: () => ({
    accessToken: "token",
    authContextVersion: 1,
    serverOrigin: "",
    profileId: mocks.activeProfile,
    profileToken: null,
  }),
  isCapturedProfileAuthorityActive: (snapshot: { profileId: string }) =>
    snapshot.profileId === mocks.activeProfile,
}));

vi.mock("sonner", () => ({
  toast: {
    error: (...args: unknown[]) => mocks.toastError(...args),
    success: (...args: unknown[]) => mocks.toastSuccess(...args),
  },
}));

import { canMarkNotInterested, useToggleNotInterested } from "./notInterested";

function noContent() {
  return {
    res: new Response(null, { status: 204 }),
    requestProfileId: "p-owner",
    requestProfileToken: null,
  };
}

function homeRow(sectionType: string): HomeSectionItemsResponse {
  return {
    section: {
      id: sectionType,
      section_type: sectionType,
      total_count: 2,
      items: [{ content_id: "movie:heat" }, { content_id: "movie:other" }],
    },
  } as unknown as HomeSectionItemsResponse;
}

function rowIDs(queryClient: QueryClient, sectionType: string) {
  return queryClient
    .getQueryData<HomeSectionItemsResponse>(sectionKeys.homeItems(sectionType))
    ?.section.items.map((item) => item.content_id);
}

describe("useToggleNotInterested", () => {
  let queryClient: QueryClient;

  beforeEach(() => {
    queryClient = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
    mocks.fetchWithSession.mockReset();
    mocks.fetchWithSession.mockImplementation(() => Promise.resolve(noContent()));
    mocks.toastSuccess.mockReset();
    mocks.activeProfile = "p-owner";
    mocks.toastError.mockReset();
    for (const sectionType of ["most_watched", "recommended_for_you", "continue_watching"]) {
      queryClient.setQueryData(sectionKeys.homeItems(sectionType), homeRow(sectionType));
    }
  });

  afterEach(cleanup);

  function wrapper({ children }: { children: ReactNode }) {
    return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
  }

  it("marks the title, takes it off recommendation and discovery rows only, and offers undo", async () => {
    const { result } = renderHook(() => useToggleNotInterested("movie:heat"), { wrapper });

    await act(() => result.current.mutateAsync(false));

    const [url, init] = mocks.fetchWithSession.mock.calls[0] as [string, RequestInit];
    expect(url).toContain("/api/v2/not-interested/movie%3Aheat");
    expect(init.method).toBe("PUT");
    expect(rowIDs(queryClient, "most_watched")).toEqual(["movie:other"]);
    expect(rowIDs(queryClient, "recommended_for_you")).toEqual(["movie:other"]);
    expect(rowIDs(queryClient, "continue_watching")).toEqual(["movie:heat", "movie:other"]);

    const [, options] = mocks.toastSuccess.mock.calls[0] as [
      string,
      { action: { label: string; onClick: () => void } },
    ];
    expect(options.action.label).toBe("Undo");
    act(() => options.action.onClick());
    await waitFor(() => expect(mocks.fetchWithSession).toHaveBeenCalledTimes(2));
    expect((mocks.fetchWithSession.mock.calls[1] as [string, RequestInit])[1].method).toBe(
      "DELETE",
    );
  });

  it("undoes a mark as the profile that made it after a profile switch", async () => {
    const { result } = renderHook(() => useToggleNotInterested("movie:heat"), { wrapper });
    await act(() => result.current.mutateAsync(false));
    const [, options] = mocks.toastSuccess.mock.calls[0] as [
      string,
      { action: { onClick: () => void } },
    ];

    mocks.activeProfile = "p-other";
    queryClient.setQueryData(sectionKeys.homeItems("most_watched"), homeRow("most_watched"));
    act(() => options.action.onClick());
    await waitFor(() => expect(mocks.fetchWithSession).toHaveBeenCalledTimes(2));

    const [url, init] = mocks.fetchWithSession.mock.calls[1] as [string, RequestInit];
    expect(url).toContain("/api/v2/not-interested/movie%3Aheat");
    expect(init.method).toBe("DELETE");
    expect((init.headers as Record<string, string>)["X-Profile-Id"]).toBe("p-owner");
    // The other profile's caches are left alone.
    expect(rowIDs(queryClient, "most_watched")).toEqual(["movie:heat", "movie:other"]);
  });

  it("clears a mark without touching the rows", async () => {
    const { result } = renderHook(() => useToggleNotInterested("movie:heat"), { wrapper });

    await act(() => result.current.mutateAsync(true));

    expect((mocks.fetchWithSession.mock.calls[0] as [string, RequestInit])[1].method).toBe(
      "DELETE",
    );
    expect(rowIDs(queryClient, "most_watched")).toEqual(["movie:heat", "movie:other"]);
  });

  it("is offered for movies and series only", () => {
    expect(canMarkNotInterested("movie")).toBe(true);
    expect(canMarkNotInterested("series")).toBe(true);
    expect(canMarkNotInterested("episode")).toBe(false);
    expect(canMarkNotInterested("season")).toBe(false);
  });
});
