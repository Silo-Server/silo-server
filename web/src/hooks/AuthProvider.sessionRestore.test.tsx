import { act, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { getAccessToken, setAccessToken } from "@/api/client";
import { queryClient } from "@/lib/query-client";
import OAuthComplete from "@/pages/OAuthComplete";
import { storage } from "@/utils/storage";
import { AuthProvider, useAuth } from "./useAuth";

// The real session client runs underneath: the fence under test is its
// auth-context generation, which a mocked client would not advance.

interface HeldRequest {
  operation: string;
  authorization: string | null;
  body: unknown;
  answered: boolean;
  respond: (status: number, body: unknown) => void;
}

/** Answers the public setup reads at once and holds every other request until the test answers it. */
function createHeldServer() {
  const requests: HeldRequest[] = [];
  const fetchImpl = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const raw = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
    const url = new URL(raw, "http://silo.test");
    const operation = `${(init?.method ?? "GET").toUpperCase()} ${url.pathname}`;
    return new Promise<Response>((resolve) => {
      const request: HeldRequest = {
        operation,
        authorization: new Headers(init?.headers).get("Authorization"),
        body: typeof init?.body === "string" ? JSON.parse(init.body) : undefined,
        answered: false,
        respond: (status, body) => {
          request.answered = true;
          resolve(
            new Response(JSON.stringify(body), {
              status,
              headers: {
                "Content-Type": status >= 400 ? "application/problem+json" : "application/json",
              },
            }),
          );
        },
      };
      requests.push(request);
      if (operation === "GET /api/v2/system/setup") {
        request.respond(200, { needs_setup: false, wizard_completed: true });
      } else if (operation === "GET /api/v2/auth/providers") {
        request.respond(200, { items: [] });
      }
    });
  }) as unknown as typeof fetch;

  async function held(operation: string, authorization?: string): Promise<HeldRequest> {
    let found: HeldRequest | undefined;
    await waitFor(() => {
      found = requests.find(
        (request) =>
          !request.answered &&
          request.operation === operation &&
          (authorization === undefined || request.authorization === authorization),
      );
      expect(found, requests.map((request) => request.operation).join("\n")).toBeDefined();
    });
    return found as HeldRequest;
  }

  async function answer(request: HeldRequest, status: number, body: unknown) {
    await act(async () => {
      request.respond(status, body);
    });
  }

  return { fetch: fetchImpl, held, answer };
}

function AuthProbe() {
  const { user, loading } = useAuth();
  return (
    <div data-testid="auth">{`${loading ? "restoring" : "restored"}:${user?.username ?? "none"}`}</div>
  );
}

function Harness({ oauthPage }: { oauthPage: boolean }) {
  return (
    <MemoryRouter initialEntries={["/login/oauth-complete"]}>
      <AuthProvider>
        <AuthProbe />
        <Routes>
          <Route path="/login/oauth-complete" element={oauthPage ? <OAuthComplete /> : null} />
          <Route path="/" element={null} />
        </Routes>
      </AuthProvider>
    </MemoryRouter>
  );
}

const laura = { id: "1", username: "laura", email: "", role: "user", permissions: [] };
const sam = { id: "2", username: "sam", email: "", role: "user", permissions: [] };

describe("AuthProvider session restore", () => {
  let server: ReturnType<typeof createHeldServer>;

  beforeEach(() => {
    localStorage.clear();
    sessionStorage.clear();
    queryClient.clear();
    setAccessToken(null);
    server = createHeldServer();
    vi.stubGlobal("fetch", server.fetch);
    // The browser still holds laura's session from an earlier visit, and
    // comes back from the identity provider signed in as sam.
    storage.set(storage.KEYS.REFRESH_TOKEN, "refresh-laura");
    window.history.replaceState(null, "", "/login/oauth-complete?code=code-sam");
  });

  afterEach(() => {
    queryClient.clear();
    setAccessToken(null);
    vi.unstubAllGlobals();
    window.history.replaceState(null, "", "/");
  });

  /**
   * Boots with laura's stored session, completes sam's OAuth sign-in while
   * the restore's read of laura's account is still in flight, then answers
   * that read with `lateAnswer`.
   */
  async function completeOAuthDuringRestore(lateAnswer: { status: number; body: unknown }) {
    const view = render(<Harness oauthPage={false} />);
    const refresh = await server.held("POST /api/v2/auth/refresh");
    expect(refresh.body).toEqual({ refresh_token: "refresh-laura" });

    // The completion page is a lazy route in the app, so it mounts after the
    // provider's boot effect has started the restore.
    view.rerender(<Harness oauthPage />);
    await server.answer(refresh, 200, {
      access_token: "access-laura",
      refresh_token: "refresh-laura-2",
      expires_in: 3600,
    });
    const lauraRead = await server.held("GET /api/v2/account/me", "Bearer access-laura");

    await server.answer(await server.held("POST /api/v2/auth/oauth/complete"), 200, {
      access_token: "access-sam",
      refresh_token: "refresh-sam",
      expires_in: 3600,
      next: "/",
    });
    await server.answer(await server.held("GET /api/v2/account/me", "Bearer access-sam"), 200, sam);
    await waitFor(() => expect(screen.getByTestId("auth")).toHaveTextContent(/:sam$/));

    await server.answer(lauraRead, lateAnswer.status, lateAnswer.body);
    await waitFor(() => expect(screen.getByTestId("auth")).toHaveTextContent(/^restored:/));
  }

  it("keeps the OAuth account when the earlier session's account read answers late", async () => {
    await completeOAuthDuringRestore({ status: 200, body: laura });

    expect(screen.getByTestId("auth")).toHaveTextContent("restored:sam");
    expect(getAccessToken()).toBe("access-sam");
    expect(storage.get(storage.KEYS.REFRESH_TOKEN)).toBe("refresh-sam");
  });

  it("keeps the OAuth session's tokens when the earlier session's restore fails late", async () => {
    await completeOAuthDuringRestore({
      status: 500,
      body: {
        type: "https://siloserver.org/docs/api/v2/problems/internal_error",
        title: "Internal error",
        status: 500,
      },
    });

    expect(screen.getByTestId("auth")).toHaveTextContent("restored:sam");
    expect(getAccessToken()).toBe("access-sam");
    expect(storage.get(storage.KEYS.REFRESH_TOKEN)).toBe("refresh-sam");
  });

  it("keeps the OAuth session when it lands before the earlier session's refresh answers", async () => {
    // Mounted with the provider, the page's exchange goes out before the
    // restore starts, so it does not wait for the restore.
    render(<Harness oauthPage />);
    const refresh = await server.held("POST /api/v2/auth/refresh");

    await server.answer(await server.held("POST /api/v2/auth/oauth/complete"), 200, {
      access_token: "access-sam",
      refresh_token: "refresh-sam",
      expires_in: 3600,
      next: "/",
    });
    await server.answer(await server.held("GET /api/v2/account/me", "Bearer access-sam"), 200, sam);
    await waitFor(() => expect(screen.getByTestId("auth")).toHaveTextContent(/:sam$/));

    // The client discards the overtaken exchange, so the restore fails.
    await server.answer(refresh, 200, {
      access_token: "access-laura",
      refresh_token: "refresh-laura-2",
      expires_in: 3600,
    });
    await waitFor(() => expect(screen.getByTestId("auth")).toHaveTextContent(/^restored:/));

    expect(screen.getByTestId("auth")).toHaveTextContent("restored:sam");
    expect(getAccessToken()).toBe("access-sam");
    expect(storage.get(storage.KEYS.REFRESH_TOKEN)).toBe("refresh-sam");
  });
});
