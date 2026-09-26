import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import getAccountPasswordCapabilityOk from "../../../../contracts/api/v2/fixtures/get_account_password_capability_ok.json";
import changePasswordPermissionDenied from "../../../../contracts/api/v2/fixtures/change_password_permission_denied.json";

import {
  StaleApiRequestContextError,
  setAccessToken,
  setProfileId,
  setProfileToken,
  setRefreshToken,
} from "@/api/client";
import { useAccountPasswordCapability, useChangeAccountPassword } from "./account";

function createHarness() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
  );
  return { wrapper };
}

function lastRequest(fetchMock: ReturnType<typeof vi.fn<typeof fetch>>) {
  const call = fetchMock.mock.calls[fetchMock.mock.calls.length - 1];
  if (!call) throw new Error("fetch was not called");
  return {
    url: String(call[0]),
    init: call[1] as RequestInit & { headers: Record<string, string> },
  };
}

const passwordChange = {
  current_password: "old password",
  new_password: "new password",
};

beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken("account-token");
  setRefreshToken(null);
  setProfileId(null);
  setProfileToken(null);
});

describe("useAccountPasswordCapability", () => {
  it("reads the capability from the v2 contract", async () => {
    const fetchMock = vi.fn<typeof fetch>(
      async () =>
        new Response(JSON.stringify(getAccountPasswordCapabilityOk), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
    );
    vi.stubGlobal("fetch", fetchMock);
    const { result } = renderHook(() => useAccountPasswordCapability(), createHarness());

    await waitFor(() => expect(result.current.isSuccess).toBe(true));

    expect(result.current.data).toEqual(getAccountPasswordCapabilityOk);
    expect(result.current.data?.allowed).toBe(true);
    const { url, init } = lastRequest(fetchMock);
    expect(url).toBe("/api/v2/account/password/capability");
    expect(init.method).toBe("GET");
    expect(init.headers.Authorization).toBe("Bearer account-token");
  });
});

describe("useChangeAccountPassword", () => {
  it("uses an account-level request when no profile is selected", async () => {
    const fetchMock = vi.fn<typeof fetch>(async () => new Response(null, { status: 204 }));
    vi.stubGlobal("fetch", fetchMock);
    const { result } = renderHook(() => useChangeAccountPassword(), createHarness());

    await act(() => result.current.mutateAsync(passwordChange));

    const { url, init } = lastRequest(fetchMock);
    expect(url).toBe("/api/v2/account/password");
    expect(init.method).toBe("POST");
    expect(init.body).toBe(JSON.stringify(passwordChange));
    expect(init.headers.Authorization).toBe("Bearer account-token");
    expect(init.headers["X-Profile-Id"]).toBeUndefined();
  });

  it("retains captured profile authority when a profile is selected", async () => {
    setProfileId("profile-1");
    setProfileToken("pin-token");
    const fetchMock = vi.fn<typeof fetch>(async () => {
      // A household profile switch while the write is in flight must not
      // re-author it: the headers were fixed when the user submitted.
      setProfileId("profile-2");
      setProfileToken(null);
      return new Response(null, { status: 204 });
    });
    vi.stubGlobal("fetch", fetchMock);
    const { result } = renderHook(() => useChangeAccountPassword(), createHarness());

    await act(() => result.current.mutateAsync(passwordChange));

    const { init } = lastRequest(fetchMock);
    expect(init.headers.Authorization).toBe("Bearer account-token");
    expect(init.headers["X-Profile-Id"]).toBe("profile-1");
    expect(init.headers["X-Profile-Token"]).toBe("pin-token");
  });

  it("surfaces the committed permission_denied problem", async () => {
    setProfileId("p-owner");
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>(
        async () =>
          new Response(JSON.stringify(changePasswordPermissionDenied), {
            status: 403,
            headers: { "Content-Type": "application/problem+json" },
          }),
      ),
    );
    const { result } = renderHook(() => useChangeAccountPassword(), createHarness());

    await expect(act(() => result.current.mutateAsync(passwordChange))).rejects.toMatchObject({
      name: "V2ProblemError",
      status: 403,
      problemType: "permission_denied",
    });
  });

  it("rejects the write when the account changes while it is in flight", async () => {
    setProfileId("profile-1");
    const fetchMock = vi.fn<typeof fetch>(async () => {
      // A logout racing the queued request: the answer must not be trusted.
      setAccessToken(null);
      return new Response(null, { status: 204 });
    });
    vi.stubGlobal("fetch", fetchMock);
    const { result } = renderHook(() => useChangeAccountPassword(), createHarness());

    await expect(act(() => result.current.mutateAsync(passwordChange))).rejects.toBeInstanceOf(
      StaleApiRequestContextError,
    );
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });
});
