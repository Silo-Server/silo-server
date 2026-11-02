import { act, render, screen, waitFor } from "@testing-library/react";
import { useState } from "react";
import { MemoryRouter, Route, Routes } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import authenticationRequired from "../../../contracts/api/v2/fixtures/authentication_required.json";

import { getAccessToken, setAccessToken } from "@/api/client";
import {
  clearStoredImpersonationAdminSession,
  loadStoredImpersonationAdminSession,
  saveStoredImpersonationAdminSession,
} from "@/lib/impersonationSession";
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
            new Response(status === 204 ? null : JSON.stringify(body), {
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

  return { fetch: fetchImpl, requests, held, answer };
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

  it("keeps the stored session when the boot refresh meets a provider outage, and retries", async () => {
    function OutageProbe() {
      const {
        user,
        loading,
        sessionRestoreUnavailable,
        sessionRestoreProviderUnavailable,
        retrySessionRestore,
      } = useAuth();
      return (
        <>
          <div data-testid="auth">{`${loading ? "restoring" : "restored"}:${user?.username ?? "none"}`}</div>
          <div data-testid="outage">{String(sessionRestoreUnavailable)}</div>
          <div data-testid="provider-outage">{String(sessionRestoreProviderUnavailable)}</div>
          <button type="button" onClick={retrySessionRestore}>
            retry
          </button>
        </>
      );
    }
    render(
      <MemoryRouter initialEntries={["/"]}>
        <AuthProvider>
          <OutageProbe />
        </AuthProvider>
      </MemoryRouter>,
    );
    await server.answer(await server.held("POST /api/v2/auth/refresh"), 503, {
      type: "https://siloserver.org/problems/provider_unavailable",
      title: "Provider unavailable",
      status: 503,
      detail: "The sign-in provider could not be reached; the session stays valid.",
    });
    await waitFor(() => expect(screen.getByTestId("auth")).toHaveTextContent("restored:none"));
    expect(screen.getByTestId("outage")).toHaveTextContent("true");
    expect(screen.getByTestId("provider-outage")).toHaveTextContent("true");
    expect(storage.get(storage.KEYS.REFRESH_TOKEN)).toBe("refresh-laura");

    // Once the provider is back, a retry restores the same session.
    await act(async () => {
      screen.getByRole("button", { name: "retry" }).click();
    });
    const retry = await server.held("POST /api/v2/auth/refresh");
    expect(retry.body).toEqual({ refresh_token: "refresh-laura" });
    await server.answer(retry, 200, {
      access_token: "access-laura",
      refresh_token: "refresh-laura-2",
      expires_in: 3600,
    });
    await server.answer(
      await server.held("GET /api/v2/account/me", "Bearer access-laura"),
      200,
      laura,
    );
    await waitFor(() => expect(screen.getByTestId("auth")).toHaveTextContent("restored:laura"));
    expect(screen.getByTestId("outage")).toHaveTextContent("false");
  });

  it("keeps the stored session through a server outage without blaming the provider", async () => {
    function OutageProbe() {
      const { loading, sessionRestoreUnavailable, sessionRestoreProviderUnavailable } = useAuth();
      return (
        <div data-testid="outage">
          {`${loading ? "restoring" : "restored"}:${String(sessionRestoreUnavailable)}:${String(sessionRestoreProviderUnavailable)}`}
        </div>
      );
    }
    render(
      <MemoryRouter initialEntries={["/"]}>
        <AuthProvider>
          <OutageProbe />
        </AuthProvider>
      </MemoryRouter>,
    );
    await server.answer(await server.held("POST /api/v2/auth/refresh"), 503, {
      type: "https://siloserver.org/problems/dependency_unavailable",
      title: "Dependency unavailable",
      status: 503,
      detail: "The session store is unavailable.",
    });
    await waitFor(() =>
      expect(screen.getByTestId("outage")).toHaveTextContent("restored:true:false"),
    );
    expect(storage.get(storage.KEYS.REFRESH_TOKEN)).toBe("refresh-laura");
  });

  it("discards the stored session when the boot refresh is refused", async () => {
    render(<Harness oauthPage={false} />);
    await server.answer(await server.held("POST /api/v2/auth/refresh"), 401, {
      type: "https://siloserver.org/problems/session_expired",
      title: "Session expired",
      status: 401,
      detail: "The session has ended.",
    });
    await waitFor(() => expect(screen.getByTestId("auth")).toHaveTextContent("restored:none"));
    expect(storage.get(storage.KEYS.REFRESH_TOKEN)).toBeNull();
  });
});

describe("AuthProvider returning to the preserved administrator session", () => {
  let server: ReturnType<typeof createHeldServer>;

  const admin = { id: "9", username: "admin", email: "", role: "admin", permissions: [] };
  const viewedAsSam = {
    ...sam,
    impersonation: { active: true, impersonator_user_id: "9", impersonator_username: "admin" },
  };
  const internalError = {
    type: "https://siloserver.org/docs/api/v2/problems/internal_error",
    title: "Internal error",
    status: 500,
  };

  function ImpersonationProbe() {
    const { user, loading, endImpersonation, login, logout } = useAuth();
    const [outcome, setOutcome] = useState("pending");
    return (
      <>
        <div data-testid="auth">{`${loading ? "restoring" : "restored"}:${user?.username ?? "none"}`}</div>
        <div data-testid="outcome">{outcome}</div>
        <button
          type="button"
          onClick={() => {
            endImpersonation().then(
              (ended) => setOutcome(ended ? "ended" : "overtaken"),
              () => setOutcome("failed"),
            );
          }}
        >
          end
        </button>
        <button type="button" onClick={() => void login("laura", "pw").catch(() => {})}>
          login
        </button>
        <button type="button" onClick={logout}>
          logout
        </button>
      </>
    );
  }

  function click(name: string) {
    return act(async () => {
      screen.getByRole("button", { name }).click();
    });
  }

  beforeEach(() => {
    localStorage.clear();
    sessionStorage.clear();
    queryClient.clear();
    setAccessToken(null);
    server = createHeldServer();
    vi.stubGlobal("fetch", server.fetch);
    saveStoredImpersonationAdminSession({
      accessToken: "access-admin",
      refreshToken: "refresh-admin",
      returnPath: "/admin/users/2",
    });
  });

  afterEach(() => {
    queryClient.clear();
    setAccessToken(null);
    vi.unstubAllGlobals();
  });

  /** Boots signed in as sam, whom the administrator is viewing as. */
  async function startViewingAsSam() {
    storage.set(storage.KEYS.REFRESH_TOKEN, "refresh-sam");
    render(
      <MemoryRouter initialEntries={["/"]}>
        <AuthProvider>
          <ImpersonationProbe />
        </AuthProvider>
      </MemoryRouter>,
    );
    await server.answer(await server.held("POST /api/v2/auth/refresh"), 200, {
      access_token: "access-sam",
      refresh_token: "refresh-sam",
      expires_in: 3600,
    });
    await server.answer(
      await server.held("GET /api/v2/account/me", "Bearer access-sam"),
      200,
      viewedAsSam,
    );
    await waitFor(() => expect(screen.getByTestId("auth")).toHaveTextContent("restored:sam"));
  }

  const overtakers = {
    "a sign-out": {
      outcome: "overtaken",
      overtake: () => click("logout"),
      expectSession: () => {
        expect(screen.getByTestId("auth")).toHaveTextContent("restored:none");
        expect(getAccessToken()).toBeNull();
        expect(storage.get(storage.KEYS.REFRESH_TOKEN)).toBeNull();
      },
    },
    "another sign-in": {
      outcome: "overtaken",
      overtake: async () => {
        await click("login");
        await server.answer(await server.held("POST /api/v2/auth/login"), 200, {
          access_token: "access-laura",
          refresh_token: "refresh-laura",
          expires_in: 3600,
          user: laura,
        });
        await waitFor(() => expect(screen.getByTestId("auth")).toHaveTextContent(":laura"));
      },
      expectSession: () => {
        expect(screen.getByTestId("auth")).toHaveTextContent("restored:laura");
        expect(getAccessToken()).toBe("access-laura");
        expect(storage.get(storage.KEYS.REFRESH_TOKEN)).toBe("refresh-laura");
      },
    },
    // In the cases below another tab finished the impersonation first. This
    // tab's own session is still current, so it follows the browser's stored
    // session, as a reload would, and leaves that session in place.
    "a sign-out in another tab": {
      outcome: "ended",
      overtake: async () => {
        storage.remove(storage.KEYS.REFRESH_TOKEN);
        clearStoredImpersonationAdminSession();
      },
      expectSession: () => {
        expect(screen.getByTestId("auth")).toHaveTextContent("restored:none");
        expect(getAccessToken()).toBeNull();
        expect(storage.get(storage.KEYS.REFRESH_TOKEN)).toBeNull();
      },
    },
    "another tab restoring the administrator": {
      outcome: "ended",
      overtake: async () => {
        storage.set(storage.KEYS.REFRESH_TOKEN, "refresh-admin-tab-2");
        clearStoredImpersonationAdminSession();
      },
      settle: async () => {
        const refresh = await server.held("POST /api/v2/auth/refresh");
        expect(refresh.body).toEqual({ refresh_token: "refresh-admin-tab-2" });
        await server.answer(refresh, 200, {
          access_token: "access-admin-tab-2",
          refresh_token: "refresh-admin-tab-2",
          expires_in: 3600,
        });
        await server.answer(
          await server.held("GET /api/v2/account/me", "Bearer access-admin-tab-2"),
          200,
          admin,
        );
        await waitFor(() => expect(screen.getByTestId("auth")).toHaveTextContent("restored:admin"));
      },
      expectSession: () => {
        expect(getAccessToken()).toBe("access-admin-tab-2");
        expect(storage.get(storage.KEYS.REFRESH_TOKEN)).toBe("refresh-admin-tab-2");
      },
    },
  } satisfies Record<
    string,
    {
      outcome: string;
      overtake: () => Promise<void>;
      settle?: () => Promise<void>;
      expectSession: () => void;
    }
  >;

  it.each([
    ["a sign-out", "the administrator's account read", 200, admin],
    ["another sign-in", "the administrator's account read", 500, internalError],
    ["another sign-in", "the end request", 204, null],
    ["a sign-out in another tab", "the administrator's account read", 200, admin],
    ["another tab restoring the administrator", "the administrator's account read", 200, admin],
    [
      "another tab restoring the administrator",
      "the administrator's account read",
      500,
      internalError,
    ],
    ["another tab restoring the administrator", "the end request", 204, null],
  ] as const)(
    "leaves %s alone when %s answers %i late",
    async (overtaker, lateRequest, status, body) => {
      await startViewingAsSam();
      await click("end");
      const end = await server.held("POST /api/v2/auth/impersonation/end", "Bearer access-sam");
      let late = end;
      if (lateRequest === "the administrator's account read") {
        await server.answer(end, 204, null);
        late = await server.held("GET /api/v2/account/me", "Bearer access-admin");
      }

      await overtakers[overtaker].overtake();
      await server.answer(late, status, body);
      const expected: { outcome: string; settle?: () => Promise<void>; expectSession: () => void } =
        overtakers[overtaker];
      await waitFor(() =>
        expect(screen.getByTestId("outcome")).toHaveTextContent(expected.outcome),
      );
      await expected.settle?.();

      expected.expectSession();
      expect(loadStoredImpersonationAdminSession()).toBeNull();
    },
  );

  it("returns to the administrator once when the end request's session was revoked", async () => {
    await startViewingAsSam();
    await click("end");
    await server.answer(
      await server.held("POST /api/v2/auth/impersonation/end", "Bearer access-sam"),
      401,
      authenticationRequired,
    );
    // The refused refresh ends sam's session, which starts the session
    // rejection's own return to the administrator.
    await server.answer(await server.held("POST /api/v2/auth/refresh"), 401, {
      type: "https://siloserver.org/problems/session_expired",
      title: "Session expired",
      status: 401,
    });
    await server.answer(
      await server.held("GET /api/v2/account/me", "Bearer access-admin"),
      200,
      admin,
    );
    await waitFor(() => expect(screen.getByTestId("outcome")).toHaveTextContent("ended"));

    expect(screen.getByTestId("auth")).toHaveTextContent("restored:admin");
    expect(getAccessToken()).toBe("access-admin");
    expect(storage.get(storage.KEYS.REFRESH_TOKEN)).toBe("refresh-admin");
    const adminReads = server.requests.filter(
      (request) =>
        request.operation === "GET /api/v2/account/me" &&
        request.authorization === "Bearer access-admin",
    );
    expect(adminReads).toHaveLength(1);
  });

  it("keeps another tab's restored administrator when the boot recovery reads late", async () => {
    // This tab boots on the impersonated session, which the server has ended.
    storage.set(storage.KEYS.REFRESH_TOKEN, "refresh-sam");
    render(
      <MemoryRouter initialEntries={["/"]}>
        <AuthProvider>
          <ImpersonationProbe />
        </AuthProvider>
      </MemoryRouter>,
    );
    await server.answer(await server.held("POST /api/v2/auth/refresh"), 401, {
      type: "https://siloserver.org/problems/session_expired",
      title: "Session expired",
      status: 401,
    });
    const adminRead = await server.held("GET /api/v2/account/me", "Bearer access-admin");

    await overtakers["another tab restoring the administrator"].overtake();
    await server.answer(adminRead, 200, admin);
    await overtakers["another tab restoring the administrator"].settle();

    overtakers["another tab restoring the administrator"].expectSession();
  });

  it("keeps a sign-in that lands while the boot recovery reads the administrator's account", async () => {
    render(
      <MemoryRouter initialEntries={["/"]}>
        <AuthProvider>
          <ImpersonationProbe />
        </AuthProvider>
      </MemoryRouter>,
    );
    const adminRead = await server.held("GET /api/v2/account/me", "Bearer access-admin");

    await overtakers["another sign-in"].overtake();
    await server.answer(adminRead, 200, admin);
    await waitFor(() => expect(screen.getByTestId("auth")).toHaveTextContent(/^restored:/));

    overtakers["another sign-in"].expectSession();
    expect(loadStoredImpersonationAdminSession()).toBeNull();
  });
});
