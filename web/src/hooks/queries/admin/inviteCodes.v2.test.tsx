// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setRefreshToken } from "@/api/client";
import { installPolicyStorageMocks, jsonResponse } from "@/pages/admin-policy/policyTestUtils";
import { useAdminInviteCodes, useCreateInviteCode, useTopUpInviteCode } from "./inviteCodes";
vi.mock("sonner", () => ({ toast: { error: vi.fn(), success: vi.fn() } }));
beforeEach(() => {
  installPolicyStorageMocks();
  setAccessToken("access");
  setRefreshToken("refresh");
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});
function wrapper({ children }: { children: ReactNode }) {
  return (
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: 3 } } })
      }
    >
      {children}
    </QueryClientProvider>
  );
}
it("keeps the selected creation code and never replays a top-up on authentication failure", async () => {
  const fetchMock = vi.fn<typeof fetch>(async () =>
    jsonResponse(
      {
        type: "https://siloserver.org/docs/api/v2/problems/authentication_required",
        status: 401,
        title: "Authentication required",
      },
      401,
    ),
  );
  vi.stubGlobal("fetch", fetchMock);
  const { result } = renderHook(
    () => ({ create: useCreateInviteCode(), topup: useTopUpInviteCode() }),
    { wrapper },
  );
  await act(async () => {
    await expect(
      result.current.create.mutateAsync({ code: "SELECTED", label: "Fixture", max_uses: 3 }),
    ).rejects.toThrow();
  });
  await act(async () => {
    await expect(
      result.current.topup.mutateAsync({ id: "9007199254740993", body: { additional_uses: 2 } }),
    ).rejects.toThrow();
  });
  expect(fetchMock).toHaveBeenCalledTimes(2);
  expect(JSON.parse(String(fetchMock.mock.calls[0]?.[1]?.body)).code).toBe("SELECTED");
  expect(String(fetchMock.mock.calls[1]?.[0])).toBe(
    "/api/v2/admin/invite-codes/9007199254740993/top-up",
  );
});
it("fetches another bounded page only when requested", async () => {
  const fetchMock = vi.fn<typeof fetch>(async (input) =>
    jsonResponse({
      items: [{ id: String(input).includes("cursor") ? "6" : "7" }],
      page: {
        has_more: !String(input).includes("cursor"),
        next_cursor: String(input).includes("cursor") ? "" : "opaque",
      },
    }),
  );
  vi.stubGlobal("fetch", fetchMock);
  const { result } = renderHook(() => useAdminInviteCodes(), { wrapper });
  await waitFor(() => expect(result.current.isSuccess).toBe(true));
  expect(fetchMock).toHaveBeenCalledTimes(1);
  await act(async () => {
    await result.current.fetchNextPage();
  });
  await waitFor(() => expect(result.current.data?.map((row) => row.id)).toEqual(["7", "6"]));
  expect(String(fetchMock.mock.calls[1]?.[0])).toBe(
    "/api/v2/admin/invite-codes?limit=50&cursor=opaque",
  );
});
