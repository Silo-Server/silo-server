import { beforeEach, describe, expect, it } from "vitest";
import { getAccessToken, setAccessToken } from "../api/client";
import { restoreUserSession } from "../api/v2/account";
import {
  clearStoredImpersonationAdminSession,
  loadStoredImpersonationAdminSession,
  saveStoredImpersonationAdminSession,
} from "./impersonationSession";

describe("impersonationSession", () => {
  beforeEach(() => {
    const sessionStorageState = new Map<string, string>();
    const localStorageState = new Map<string, string>();
    Object.defineProperty(globalThis, "sessionStorage", {
      value: {
        get length() {
          return sessionStorageState.size;
        },
        getItem: (key: string) => sessionStorageState.get(key) ?? null,
        key: (index: number) => Array.from(sessionStorageState.keys())[index] ?? null,
        setItem: (key: string, value: string) => {
          sessionStorageState.set(key, value);
        },
        removeItem: (key: string) => {
          sessionStorageState.delete(key);
        },
        clear: () => {
          sessionStorageState.clear();
        },
      } satisfies Storage,
      configurable: true,
    });

    Object.defineProperty(globalThis, "localStorage", {
      value: {
        get length() {
          return localStorageState.size;
        },
        getItem: (key: string) => localStorageState.get(key) ?? null,
        key: (index: number) => Array.from(localStorageState.keys())[index] ?? null,
        setItem: (key: string, value: string) => {
          localStorageState.set(key, value);
        },
        removeItem: (key: string) => {
          localStorageState.delete(key);
        },
        clear: () => {
          localStorageState.clear();
        },
      } satisfies Storage,
      configurable: true,
    });

    sessionStorage.clear();
    localStorage.clear();
  });

  it("round-trips preserved admin tokens and return path", () => {
    saveStoredImpersonationAdminSession({
      accessToken: "admin-access",
      refreshToken: "admin-refresh",
      returnPath: "/admin/users/42",
    });

    expect(loadStoredImpersonationAdminSession()).toEqual({
      accessToken: "admin-access",
      refreshToken: "admin-refresh",
      returnPath: "/admin/users/42",
    });
  });

  it("stores and clears the preserved admin session in localStorage to match auth scope", () => {
    saveStoredImpersonationAdminSession({
      accessToken: "admin-access",
      refreshToken: "admin-refresh",
      returnPath: "/admin/users/42",
    });

    expect(localStorage.getItem("impersonation_admin_session")).toBe(
      JSON.stringify({
        accessToken: "admin-access",
        refreshToken: "admin-refresh",
        returnPath: "/admin/users/42",
      }),
    );
    expect(sessionStorage.getItem("impersonation_admin_session")).toBeNull();

    clearStoredImpersonationAdminSession();

    expect(localStorage.getItem("impersonation_admin_session")).toBeNull();
  });

  it("validates a stored admin session without mutating global auth state", async () => {
    setAccessToken("impersonated-access");

    const fetchCalls: Array<{ url: string; authorization: string | null }> = [];
    const fetchMock: typeof fetch = async (input, init) => {
      const url = String(input);
      const headers = new Headers(init?.headers);
      fetchCalls.push({
        url,
        authorization: headers.get("Authorization"),
      });

      if (
        url === "/api/v2/account/me" &&
        headers.get("Authorization") === "Bearer expired-admin-access"
      ) {
        return new Response(
          JSON.stringify({
            type: "https://silo.example/problems/authentication_required",
            title: "Authentication required",
            status: 401,
            detail: "expired",
          }),
          {
            status: 401,
            headers: { "Content-Type": "application/problem+json" },
          },
        );
      }

      if (url.endsWith("/auth/refresh")) {
        return new Response(
          JSON.stringify({
            access_token: "fresh-admin-access",
            refresh_token: "fresh-admin-refresh",
            expires_in: 3600,
          }),
          {
            status: 200,
            headers: { "Content-Type": "application/json" },
          },
        );
      }

      if (
        url === "/api/v2/account/me" &&
        headers.get("Authorization") === "Bearer fresh-admin-access"
      ) {
        return new Response(
          JSON.stringify({
            id: "1",
            username: "admin",
            email: "admin@example.com",
            role: "admin",
            permissions: [],
            download_allowed: true,
          }),
          {
            status: 200,
            headers: { "Content-Type": "application/json" },
          },
        );
      }

      throw new Error(`Unhandled fetch call: ${url}`);
    };

    const restoredSession = await restoreUserSession({
      accessToken: "expired-admin-access",
      refreshToken: "admin-refresh",
      fetchImpl: fetchMock,
    });

    expect(restoredSession).toEqual({
      accessToken: "fresh-admin-access",
      refreshToken: "fresh-admin-refresh",
      user: {
        id: 1,
        username: "admin",
        email: "admin@example.com",
        role: "admin",
        permissions: [],
        download_allowed: true,
        impersonation: null,
      },
    });
    expect(getAccessToken()).toBe("impersonated-access");
    expect(fetchCalls).toEqual([
      {
        url: "/api/v2/account/me",
        authorization: "Bearer expired-admin-access",
      },
      {
        url: "/api/v1/auth/refresh",
        authorization: null,
      },
      {
        url: "/api/v2/account/me",
        authorization: "Bearer fresh-admin-access",
      },
    ]);
  });
});
