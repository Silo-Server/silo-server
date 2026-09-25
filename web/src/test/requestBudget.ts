/**
 * A request-budget harness for app-level boot tests.
 *
 * It replaces the global `fetch` with a small in-memory model of the Silo
 * server and releases responses one wave at a time: every request issued
 * while the previous wave's responses were being handled belongs to the next
 * wave. A wave is one serial network round trip, so the wave in which a
 * request starts is the number of round trips the app waited before it could
 * send it. The counts are deterministic because nothing depends on latency.
 *
 * The real session client (`fetchWithSession`, the v2 boundary, the refresh
 * single-flight) runs on top of it, so a request sent before the app holds an
 * access token is answered 401 exactly as the server would answer it.
 */
import { act } from "@testing-library/react";
import { vi } from "vitest";
import { v2Operations } from "@/api/v2/operations";

/** One request as the fake server saw it. */
export interface RecordedRequest {
  seq: number;
  /** 1-based: the number of response waves released before this request was sent, plus one. */
  wave: number;
  method: string;
  /** Pathname plus search, e.g. `/api/v2/settings/values/effective?keys=ui.theme`. */
  url: string;
  /** The `METHOD /path` operation key from the v2 contract, when one matches. */
  operation: string | null;
  authorized: boolean;
  status?: number;
}

/** A canned answer for one v2 operation. */
export interface FakeResponse {
  status?: number;
  body?: unknown;
  headers?: Record<string, string>;
}

export type FakeRoute = FakeResponse | ((request: RecordedRequest) => FakeResponse);

/**
 * Operations the server answers without a bearer token (their OpenAPI
 * operation carries no `bearerAuth` requirement). Every other `/api/v2`
 * operation answers 401 to an anonymous request.
 */
const PUBLIC_OPERATIONS = new Set<string>([
  "GET /api/v2/system/setup",
  "GET /api/v2/auth/providers",
  "GET /api/v2/auth/signup",
  "GET /api/v2/capabilities/password-reset",
  "GET /api/v2/theme/branding",
  "GET /api/v2/theme/admin-css",
  "POST /api/v2/auth/refresh",
]);

const operationMatchers = Object.keys(v2Operations).map((key) => {
  const [method, route] = key.split(" ", 2) as [string, string];
  const pattern = new RegExp(`^${route.replace(/\{[^}]+\}/g, "[^/]+")}$`);
  return { key, method, pattern };
});

function matchOperation(method: string, pathname: string): string | null {
  return (
    operationMatchers.find((entry) => entry.method === method && entry.pattern.test(pathname))
      ?.key ?? null
  );
}

function problem(status: number, id: string, title: string): FakeResponse {
  return {
    status,
    body: {
      type: `https://siloserver.org/docs/api/v2/problems/${id}`,
      title,
      status,
    },
  };
}

function jsonResponse({ status = 200, body, headers }: FakeResponse): Response {
  const isProblem = status >= 400;
  return new Response(body === undefined ? null : JSON.stringify(body), {
    status,
    headers: {
      "Content-Type": isProblem ? "application/problem+json" : "application/json",
      ...headers,
    },
  });
}

export interface FakeServer {
  fetch: typeof fetch;
  readonly requests: readonly RecordedRequest[];
  /** Number of requests waiting for the next wave to be released. */
  pendingCount(): number;
  /** Answers every pending request; requests sent afterwards join the next wave. */
  releaseWave(): number;
  /** Refresh tokens the server accepted, in order. */
  readonly refreshTokensUsed: readonly string[];
  /**
   * Mints a token pair the server accepts, as a sign-in or an admin's
   * impersonate operation would hand one out.
   */
  issueTokens(): { access_token: string; refresh_token: string; expires_in: number };
  /**
   * Stops accepting every token issued so far, as the server does when an
   * admin disables the account or the session is revoked. A refresh with a
   * revoked token is 401 `session_expired`.
   */
  revokeSessions(): void;
  /** Stops accepting one issued token pair, leaving every other session live. */
  revokeSession(tokens: { access_token: string; refresh_token: string }): void;
}

export interface FakeServerOptions {
  /** Refresh tokens the server refuses, as it refuses a revoked session. */
  revokedRefreshTokens?: readonly string[];
}

/**
 * Builds the fake server. `routes` maps a v2 operation key to its answer;
 * an operation without a route answers 501 so an unexpected boot request is
 * visible in the log instead of silently succeeding.
 */
export function createFakeServer(
  routes: Record<string, FakeRoute>,
  { revokedRefreshTokens = [] }: FakeServerOptions = {},
): FakeServer {
  const requests: RecordedRequest[] = [];
  const refreshTokensUsed: string[] = [];
  const issuedAccessTokens = new Set<string>();
  const refusedRefreshTokens = new Set<string>(revokedRefreshTokens);
  let issuedRefreshTokens: string[] = [];
  let pending: Array<() => void> = [];
  let wave = 1;
  let tokenCounter = 0;

  function issueTokens() {
    tokenCounter += 1;
    const accessToken = `access-${tokenCounter}`;
    issuedAccessTokens.add(accessToken);
    issuedRefreshTokens.push(`refresh-${tokenCounter}`);
    return {
      access_token: accessToken,
      refresh_token: `refresh-${tokenCounter}`,
      expires_in: 3600,
    };
  }

  function answer(record: RecordedRequest, init: RequestInit | undefined): FakeResponse {
    if (record.operation === null) {
      return problem(404, "not_found", "Not found");
    }
    if (record.operation === "POST /api/v2/auth/refresh") {
      const body = JSON.parse(String(init?.body ?? "{}")) as { refresh_token?: string };
      if (body.refresh_token && refusedRefreshTokens.has(body.refresh_token)) {
        return problem(401, "session_expired", "Session has been revoked.");
      }
      if (!body.refresh_token) {
        return problem(401, "invalid_token", "Invalid or expired refresh token.");
      }
      refreshTokensUsed.push(body.refresh_token);
      return { body: issueTokens() };
    }
    const headers = new Headers(init?.headers);
    const bearer = headers.get("Authorization")?.replace(/^Bearer /, "") ?? "";
    if (!PUBLIC_OPERATIONS.has(record.operation) && !issuedAccessTokens.has(bearer)) {
      return problem(401, "authentication_required", "Authentication required");
    }
    const route = routes[record.operation];
    if (!route) {
      return problem(501, "not_implemented", `No fake route for ${record.operation}`);
    }
    return typeof route === "function" ? route(record) : route;
  }

  const fakeFetch = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const raw = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
    const url = new URL(raw, "http://silo.test");
    const method = (init?.method ?? "GET").toUpperCase();
    const headers = new Headers(init?.headers);
    const record: RecordedRequest = {
      seq: requests.length + 1,
      wave,
      method,
      url: `${url.pathname}${url.search}`,
      operation: matchOperation(method, url.pathname),
      authorized: headers.has("Authorization"),
    };
    requests.push(record);
    return new Promise<Response>((resolve) => {
      pending.push(() => {
        const response = answer(record, init);
        record.status = response.status ?? 200;
        resolve(jsonResponse(response));
      });
    });
  }) as unknown as typeof fetch;

  return {
    fetch: fakeFetch,
    requests,
    refreshTokensUsed,
    issueTokens,
    revokeSessions() {
      issuedAccessTokens.clear();
      for (const token of [...refreshTokensUsed, ...issuedRefreshTokens]) {
        refusedRefreshTokens.add(token);
      }
      issuedRefreshTokens = [];
    },
    revokeSession(tokens) {
      issuedAccessTokens.delete(tokens.access_token);
      refusedRefreshTokens.add(tokens.refresh_token);
    },
    pendingCount: () => pending.length,
    releaseWave() {
      const batch = pending;
      pending = [];
      wave += 1;
      for (const respond of batch) respond();
      return batch.length;
    },
  };
}

/**
 * Lets React render, run effects, and flush TanStack Query notifications
 * until no new request appears. Timers must be faked by the caller; only
 * zero-delay work runs here, so a retry scheduled with a backoff delay stays
 * parked until `advanceClock` releases it.
 */
export async function settle(server: FakeServer): Promise<void> {
  let quietRounds = 0;
  for (let round = 0; round < 200 && quietRounds < 5; round += 1) {
    const before = server.requests.length;
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    quietRounds = server.requests.length === before ? quietRounds + 1 : 0;
  }
}

/** Moves the fake clock forward, e.g. past a query retry's backoff delay. */
export async function advanceClock(ms: number): Promise<void> {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });
}

/** What one boot cost, measured from the recorded requests. */
export interface BootBudget {
  /**
   * Requests sent in the waves before the one that carried GET
   * /api/v2/home/layout. Requests sent alongside it are concurrent with it,
   * so they are not counted as ahead of it.
   */
  requestsBeforeHomeLayout: number;
  /** Response waves the app waited for before it could send GET /api/v2/home/layout. */
  wavesBeforeHomeLayout: number;
  /** Requests the server answered 401. */
  unauthorized: number;
  /** POST /api/v2/auth/refresh calls. */
  refreshes: number;
  /** Successful GETs repeated for a URL that already answered 2xx. */
  duplicateGets: number;
  /** Every request, in order. */
  total: number;
}

export function measureBoot(requests: readonly RecordedRequest[]): BootBudget {
  const homeLayout = requests.find((request) => request.operation === "GET /api/v2/home/layout");
  const seen = new Set<string>();
  let duplicateGets = 0;
  for (const request of requests) {
    if (request.method !== "GET" || request.status === undefined || request.status >= 300) {
      continue;
    }
    if (seen.has(request.url)) duplicateGets += 1;
    seen.add(request.url);
  }
  return {
    requestsBeforeHomeLayout: homeLayout
      ? requests.filter((request) => request.wave < homeLayout.wave).length
      : -1,
    wavesBeforeHomeLayout: homeLayout ? homeLayout.wave - 1 : -1,
    unauthorized: requests.filter((request) => request.status === 401).length,
    refreshes: requests.filter((request) => request.operation === "POST /api/v2/auth/refresh")
      .length,
    duplicateGets,
    total: requests.length,
  };
}

/** A readable one-line-per-request log for failure messages. */
export function describeRequests(requests: readonly RecordedRequest[]): string {
  return requests
    .map(
      (request) =>
        `#${request.seq} w${request.wave} ${request.method} ${request.url} -> ${request.status ?? "pending"}${request.authorized ? "" : " (anonymous)"}`,
    )
    .join("\n");
}
