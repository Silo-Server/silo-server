// @vitest-environment jsdom
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import type { ReactNode } from "react";
import {
  useLinkAccountIdentityWithCredentials,
  useStartAccountIdentityLink,
  useUnlinkAccountIdentity,
} from "./account";
import {
  setAccessToken,
  setProfileId,
  setRefreshToken,
  StaleApiRequestContextError,
} from "@/api/client";

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": status >= 400 ? "application/problem+json" : "application/json" },
  });
}
function path(input: RequestInfo | URL) {
  return new URL(
    typeof input === "string" ? input : input instanceof URL ? input.href : input.url,
    "http://silo.test",
  ).pathname;
}
function wrap({ children }: { children: ReactNode }) {
  return (
    <QueryClientProvider
      client={new QueryClient({ defaultOptions: { mutations: { retry: false } } })}
    >
      {children}
    </QueryClientProvider>
  );
}
beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken("access-A");
  setRefreshToken("refresh-A");
  setProfileId("profile-A");
});
afterEach(() => {
  cleanup();
  setAccessToken(null);
  setProfileId(null);
  setRefreshToken(null);
  vi.unstubAllGlobals();
});
function switchAccount() {
  setAccessToken("access-B");
  setRefreshToken("refresh-B");
  setProfileId("profile-B");
}
const DIRECTORY = {
  installation_id: "6",
  password: "shared-local-password",
  username: "directory-alice",
  directory_password: "directory-password",
};

it.each(["credentials", "ticket", "unlink"] as const)(
  "does not refresh or replay a rejected %s mutation as an account that replaced its caller",
  async (kind) => {
    let release: ((answer: Response) => void) | undefined;
    const sends: Array<{ path: string; authorization: string | null }> = [];
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const requestPath = path(input);
      sends.push({
        path: requestPath,
        authorization: new Headers(init?.headers).get("Authorization"),
      });
      if (sends.length === 1)
        return new Promise<Response>((resolve) => {
          release = resolve;
        });
      if (requestPath === "/api/v2/auth/refresh")
        return Promise.resolve(
          json({
            access_token: "access-B-fresh",
            refresh_token: "refresh-B-fresh",
            expires_in: 3600,
          }),
        );
      return Promise.resolve(
        json(
          { id: "77", ticket: "ticket-B", authorize_url: "https://id.example.test/authorize" },
          201,
        ),
      );
    });
    vi.stubGlobal("fetch", fetchMock);
    const { result } = renderHook(
      () => ({
        credentials: useLinkAccountIdentityWithCredentials(),
        ticket: useStartAccountIdentityLink(),
        unlink: useUnlinkAccountIdentity(),
      }),
      { wrapper: wrap },
    );
    let pending: Promise<unknown>;
    await act(async () => {
      pending =
        kind === "credentials"
          ? result.current.credentials.mutateAsync(DIRECTORY)
          : kind === "ticket"
            ? result.current.ticket.mutateAsync({
                installationId: "5",
                password: "shared-local-password",
                next: "/settings/account",
              })
            : result.current.unlink.mutateAsync("77");
      void pending.catch(() => {});
    });
    await waitFor(() => expect(sends).toHaveLength(1));
    switchAccount();
    await act(async () =>
      release!(
        json(
          {
            type: "https://siloserver.org/problems/invalid_token",
            title: "Invalid token",
            status: 401,
          },
          401,
        ),
      ),
    );
    await expect(pending!).rejects.toBeInstanceOf(StaleApiRequestContextError);
    expect(sends).toHaveLength(1);
    expect(sends[0]?.authorization).toBe("Bearer access-A");
  },
);

it("keeps a link ticket from starting in a session that replaced its issuer", async () => {
  let release: ((answer: Response) => void) | undefined;
  const fetchMock = vi.fn((input: RequestInfo | URL) =>
    path(input) === "/api/v2/account/identities/link-ticket"
      ? new Promise<Response>((resolve) => {
          release = resolve;
        })
      : Promise.resolve(json({ authorize_url: "https://id.example.test/authorize" })),
  );
  vi.stubGlobal("fetch", fetchMock);
  // Account operations also work before a household profile is selected.
  setProfileId(null);
  const { result } = renderHook(() => useStartAccountIdentityLink(), { wrapper: wrap });
  let pending: Promise<unknown>;
  await act(async () => {
    pending = result.current.mutateAsync({
      installationId: "5",
      password: "password-A",
      next: "/settings/account",
    });
    void pending.catch(() => {});
  });
  await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
  switchAccount();
  await act(async () => release!(json({ ticket: "ticket-A", expires_at: "2026-10-02T12:00:00Z" })));
  await expect(pending!).rejects.toBeInstanceOf(StaleApiRequestContextError);
  expect(fetchMock).toHaveBeenCalledTimes(1);
});

it("discards a link-start answer after the account changes before a provider redirect", async () => {
  let release: ((answer: Response) => void) | undefined;
  const fetchMock = vi.fn((input: RequestInfo | URL) =>
    path(input) === "/api/v2/account/identities/link-ticket"
      ? Promise.resolve(json({ ticket: "ticket-A", expires_at: "2026-10-02T12:00:00Z" }))
      : new Promise<Response>((resolve) => {
          release = resolve;
        }),
  );
  vi.stubGlobal("fetch", fetchMock);
  const { result } = renderHook(() => useStartAccountIdentityLink(), { wrapper: wrap });
  let pending: Promise<unknown>;
  await act(async () => {
    pending = result.current.mutateAsync({
      installationId: "5",
      password: "password-A",
      next: "/settings/account",
    });
    void pending.catch(() => {});
  });
  await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
  switchAccount();
  await act(async () => release!(json({ authorize_url: "https://id.example.test/authorize" })));
  await expect(pending!).rejects.toBeInstanceOf(StaleApiRequestContextError);
});
