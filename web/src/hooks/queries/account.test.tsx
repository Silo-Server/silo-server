import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook } from "@testing-library/react";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { ProfileRequestContextSnapshot } from "@/api/client";
import { useChangeAccountPassword } from "./account";

const apiMock = vi.hoisted(() => vi.fn());
const apiWithProfileRequestContextMock = vi.hoisted(() => vi.fn());
const captureProfileRequestContextMock = vi.hoisted(() => vi.fn());

vi.mock("@/api/client", async () => {
  const actual = await vi.importActual<typeof import("@/api/client")>("@/api/client");
  return {
    ...actual,
    api: apiMock,
    apiWithProfileRequestContext: apiWithProfileRequestContextMock,
    captureProfileRequestContext: captureProfileRequestContextMock,
  };
});

function createHarness() {
  const queryClient = new QueryClient({
    defaultOptions: { mutations: { retry: false } },
  });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
  );
  return { wrapper };
}

const passwordChange = {
  current_password: "old password",
  new_password: "new password",
};

describe("useChangeAccountPassword", () => {
  beforeEach(() => {
    apiMock.mockReset().mockResolvedValue(undefined);
    apiWithProfileRequestContextMock.mockReset().mockResolvedValue(undefined);
    captureProfileRequestContextMock.mockReset();
  });

  it("uses an account-level request when no profile is selected", async () => {
    captureProfileRequestContextMock.mockReturnValue(null);
    const { result } = renderHook(() => useChangeAccountPassword(), createHarness());

    await act(() => result.current.mutateAsync(passwordChange));

    expect(apiMock).toHaveBeenCalledWith("/auth/account/password", {
      method: "POST",
      body: JSON.stringify(passwordChange),
    });
    expect(apiWithProfileRequestContextMock).not.toHaveBeenCalled();
  });

  it("retains captured profile authority when a profile is selected", async () => {
    const requestContext: ProfileRequestContextSnapshot = {
      accessToken: "account-token",
      authContextVersion: 1,
      serverOrigin: globalThis.location.origin,
      profileId: "profile-1",
      profileToken: "pin-token",
    };
    captureProfileRequestContextMock.mockReturnValue(requestContext);
    const { result } = renderHook(() => useChangeAccountPassword(), createHarness());

    await act(() => result.current.mutateAsync(passwordChange));

    expect(apiWithProfileRequestContextMock).toHaveBeenCalledWith(
      "/auth/account/password",
      requestContext,
      {
        method: "POST",
        body: JSON.stringify(passwordChange),
      },
    );
    expect(apiMock).not.toHaveBeenCalled();
  });
});
