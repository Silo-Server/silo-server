import { act, render, screen } from "@testing-library/react";
import { useEffect, type ReactNode } from "react";
import type { createMemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import adminAccountImpersonate from "../../../contracts/api/v2/fixtures/admin_account_impersonate.json";
import eventsSocketTicket from "../../../contracts/api/v2/fixtures/events_socket_ticket.json";
import getCurrentUserOk from "../../../contracts/api/v2/fixtures/get_current_user_ok.json";
import getHomeLayoutOk from "../../../contracts/api/v2/fixtures/get_home_layout_ok.json";
import getSettingsContractCapabilitiesOk from "../../../contracts/api/v2/fixtures/get_settings_contract_capabilities_ok.json";
import listEffectiveSettingsOk from "../../../contracts/api/v2/fixtures/list_effective_settings_ok.json";
import listFavoritesOk from "../../../contracts/api/v2/fixtures/list_favorites_ok.json";
import listProfilesOk from "../../../contracts/api/v2/fixtures/list_profiles_ok.json";
import userLibraries from "../../../contracts/api/v2/fixtures/user_libraries.json";
import { setAccessToken } from "@/api/client";
import { sessionFromTokenPair, type TokenPair } from "@/api/v2/account";
import type { components } from "@/api/v2/schema";
import { profileFromV2 } from "@/hooks/queries/profiles";
import { useHomeLayout } from "@/hooks/queries/sections";
import { useAuth } from "@/hooks/useAuth";
import { queryClient } from "@/lib/query-client";
import { storage } from "@/utils/storage";
import {
  advanceClock,
  createFakeServer,
  describeRequests,
  measureBoot,
  settle,
  type FakeServer,
} from "./requestBudget";

let initialEntry = "/";
let appRouter: ReturnType<typeof createMemoryRouter> | null = null;
let homeAuth: ReturnType<typeof useAuth> | null = null;

vi.mock("react-router", async () => {
  const actual = await vi.importActual<typeof import("react-router")>("react-router");
  return {
    ...actual,
    // App builds a data router from the real history; start it at the entry
    // under test instead.
    createBrowserRouter: ((routes: Parameters<typeof actual.createMemoryRouter>[0]) => {
      appRouter = actual.createMemoryRouter(routes, {
        initialEntries: [initialEntry],
      });
      return appRouter;
    }) as typeof actual.createBrowserRouter,
  };
});

// The page chrome and the home rows are out of scope: this budget covers what
// the always-mounted shell and the route gates cost before Home can ask for
// its layout. Everything above the routed page is real.
vi.mock("@/components/Layout", () => ({
  default: ({ children }: { children: ReactNode }) => <>{children}</>,
}));

function HomeLayoutProbe() {
  const auth = useAuth();
  // Keeps the signed-in auth actions reachable for cases that act after boot.
  useEffect(() => {
    homeAuth = auth;
  }, [auth]);
  const { data } = useHomeLayout();
  return <div data-testid="home">{data ? `${data.sections.length} sections` : "loading"}</div>;
}

vi.mock("@/pages/Home", () => ({ default: HomeLayoutProbe }));
vi.mock("@/lib/routeChunkPrefetch", () => ({ prefetchRouteChunks: () => () => {} }));
vi.mock("@/player/hooks/useCodecDetection", async () => {
  const actual = await vi.importActual<typeof import("@/player/hooks/useCodecDetection")>(
    "@/player/hooks/useCodecDetection",
  );
  return { ...actual, prewarmCodecDetection: () => Promise.resolve() };
});

import App from "@/App";

/** Accepts the events socket and never opens it; the socket is not under test. */
class InertWebSocket extends EventTarget {
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSING = 2;
  static readonly CLOSED = 3;
  readonly readyState = 0;
  onopen = null;
  onmessage = null;
  onclose = null;
  onerror = null;
  send() {}
  close() {}
}

// JSON imports widen enum fields to string; the fixture is a contract Profile.
const ownerProfile = listProfilesOk.items[0]! as components["schemas"]["Profile"];

function signInReturningOwner() {
  // A returning browser: a stored refresh token and a selected profile. The
  // access token only ever lives in memory, so there is none yet.
  storage.set(storage.KEYS.REFRESH_TOKEN, "refresh-0");
  storage.set(storage.KEYS.PROFILE_ID, ownerProfile.id);
  storage.set(storage.KEYS.CURRENT_PROFILE, JSON.stringify(profileFromV2(ownerProfile)));
}

function serverRoutes() {
  return {
    "GET /api/v2/system/setup": { body: { needs_setup: false, wizard_completed: true } },
    "GET /api/v2/auth/providers": { body: { items: [] } },
    "GET /api/v2/auth/signup": { body: { enabled: false } },
    "GET /api/v2/capabilities/password-reset": { body: { revision: "r", state: "disabled" } },
    "GET /api/v2/theme/branding": { body: {} },
    "GET /api/v2/theme/admin-css": { body: {} },
    "GET /api/v2/account/me": { body: getCurrentUserOk },
    "GET /api/v2/profiles": { body: listProfilesOk },
    "GET /api/v2/settings/contract/capabilities": { body: getSettingsContractCapabilitiesOk },
    "GET /api/v2/settings/values/effective": { body: listEffectiveSettingsOk },
    "GET /api/v2/favorites": { body: listFavoritesOk },
    "GET /api/v2/user/libraries": { body: userLibraries },
    "GET /api/v2/home/layout": { body: getHomeLayoutOk },
    "GET /api/v2/onboarding/state": {
      body: { tour_id: "welcome", done: true },
      headers: { ETag: '"onboarding-1"' },
    },
    "POST /api/v2/events/ws-ticket": { body: eventsSocketTicket },
  };
}

async function releaseUntilQuiet(server: FakeServer) {
  for (let wave = 0; wave < 20; wave += 1) {
    await settle(server);
    if (server.pendingCount() === 0) return;
    server.releaseWave();
  }
  throw new Error(`boot did not settle:\n${describeRequests(server.requests)}`);
}

async function boot(server: FakeServer) {
  render(<App />);
  await releaseUntilQuiet(server);
  // Past TanStack Query's first retry backoff (1s), so a failed read parked
  // for a retry is counted too.
  await advanceClock(1_500);
  await releaseUntilQuiet(server);
}

describe("app boot request budget", () => {
  let server: FakeServer;

  beforeEach(() => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval"] });
    initialEntry = "/";
    appRouter = null;
    homeAuth = null;
    localStorage.clear();
    sessionStorage.clear();
    queryClient.clear();
    setAccessToken(null);
    server = createFakeServer(serverRoutes());
    vi.stubGlobal("fetch", server.fetch);
    vi.stubGlobal("WebSocket", InertWebSocket);
    vi.stubGlobal(
      "matchMedia",
      vi.fn((query: string) => ({
        matches: false,
        media: query,
        onchange: null,
        addEventListener: () => {},
        removeEventListener: () => {},
        addListener: () => {},
        removeListener: () => {},
        dispatchEvent: () => false,
      })),
    );
  });

  afterEach(() => {
    queryClient.clear();
    setAccessToken(null);
    vi.unstubAllGlobals();
    vi.useRealTimers();
  });

  it("restores a returning session and reaches the home layout in two waves", async () => {
    signInReturningOwner();

    await boot(server);

    const log = describeRequests(server.requests);
    expect(screen.getByTestId("home"), log).toHaveTextContent("2 sections");
    expect(measureBoot(server.requests), log).toEqual({
      requestsBeforeHomeLayout: 6,
      wavesBeforeHomeLayout: 2,
      unauthorized: 0,
      refreshes: 1,
      duplicateGets: 0,
      total: 19,
    });
    // The session restore starts beside the public setup reads, not after them.
    expect(
      server.requests.filter((request) => request.wave === 1).map((request) => request.operation),
      log,
    ).toEqual(
      expect.arrayContaining([
        "GET /api/v2/system/setup",
        "GET /api/v2/auth/providers",
        "POST /api/v2/auth/refresh",
      ]),
    );
    expect(server.refreshTokensUsed).toEqual(["refresh-0"]);
  });

  it("sends a revoked session to sign-in after one refused refresh", async () => {
    // Another device revoked this browser's session (Account sessions).
    storage.set(storage.KEYS.REFRESH_TOKEN, "revoked-0");
    storage.set(storage.KEYS.PROFILE_ID, ownerProfile.id);
    storage.set(storage.KEYS.CURRENT_PROFILE, JSON.stringify(profileFromV2(ownerProfile)));
    server = createFakeServer(serverRoutes(), { revokedRefreshTokens: ["revoked-0"] });
    vi.stubGlobal("fetch", server.fetch);

    await boot(server);

    const log = describeRequests(server.requests);
    expect(screen.getByRole("heading", { name: /sign in/i }), log).toBeInTheDocument();
    // The refused refresh is the only 401: nothing else went out on the
    // revoked session or retried it.
    expect(measureBoot(server.requests), log).toEqual({
      requestsBeforeHomeLayout: -1,
      wavesBeforeHomeLayout: -1,
      unauthorized: 1,
      refreshes: 1,
      duplicateGets: 0,
      // Includes the login page's public signup-status and password-reset
      // capability reads.
      total: 7,
    });
    expect(storage.get(storage.KEYS.REFRESH_TOKEN)).toBeNull();
  });

  it("sends a session the server stops accepting mid-use to sign-in", async () => {
    // An admin disables the account (or revokes the session) while it browses.
    signInReturningOwner();
    await boot(server);
    expect(screen.getByTestId("home"), describeRequests(server.requests)).toBeInTheDocument();

    server.revokeSessions();
    await act(async () => {
      void queryClient.invalidateQueries();
    });
    await releaseUntilQuiet(server);

    const log = describeRequests(server.requests);
    expect(screen.getByRole("heading", { name: /sign in/i }), log).toBeInTheDocument();
    expect(screen.queryByTestId("home"), log).not.toBeInTheDocument();
    expect(appRouter!.state.location.pathname, log).toBe("/login");
    expect(storage.get(storage.KEYS.REFRESH_TOKEN)).toBeNull();
    // One refused refresh is shared; nothing retries the rejected session.
    const refusedRefreshes = server.requests.filter(
      (request) => request.operation === "POST /api/v2/auth/refresh" && request.status === 401,
    );
    expect(refusedRefreshes, log).toHaveLength(1);
  });

  it("returns an admin to their own session when the viewed session is revoked", async () => {
    signInReturningOwner();
    await boot(server);
    expect(screen.getByTestId("home"), describeRequests(server.requests)).toBeInTheDocument();
    const adminRefreshToken = storage.get(storage.KEYS.REFRESH_TOKEN);

    // What AdminUserImpersonationDialog does with the impersonate answer.
    const viewedTokens = server.issueTokens();
    const pair = { ...adminAccountImpersonate, ...viewedTokens } as TokenPair;
    await act(async () => {
      homeAuth!.beginImpersonation(sessionFromTokenPair(pair), "/admin/users");
    });
    await releaseUntilQuiet(server);
    expect(storage.get(storage.KEYS.REFRESH_TOKEN)).toBe(viewedTokens.refresh_token);

    // The viewed account is disabled while the admin browses as it.
    server.revokeSession(viewedTokens);
    await act(async () => {
      void queryClient.invalidateQueries();
    });
    await releaseUntilQuiet(server);

    const log = describeRequests(server.requests);
    expect(appRouter!.state.location.pathname, log).not.toBe("/login");
    expect(screen.queryByRole("heading", { name: /sign in/i }), log).not.toBeInTheDocument();
    expect(storage.get(storage.KEYS.REFRESH_TOKEN), log).toBe(adminRefreshToken);
    expect(localStorage.getItem("impersonation_admin_session"), log).toBeNull();
    // Every request refused on the viewed session joins one recovery.
    const refusedRefreshes = server.requests.filter(
      (request) => request.operation === "POST /api/v2/auth/refresh" && request.status === 401,
    );
    expect(refusedRefreshes.length, log).toBeGreaterThan(0);
    expect(
      server.requests.filter(
        (request) =>
          request.operation === "GET /api/v2/account/me" && request.seq > refusedRefreshes[0]!.seq,
      ),
      log,
    ).toHaveLength(1);
  });

  it("keeps a sign-in that replaces the session while the admin session is restored", async () => {
    signInReturningOwner();
    await boot(server);
    const adminRefreshToken = storage.get(storage.KEYS.REFRESH_TOKEN);
    const viewedTokens = server.issueTokens();
    const pair = { ...adminAccountImpersonate, ...viewedTokens } as TokenPair;
    await act(async () => {
      homeAuth!.beginImpersonation(sessionFromTokenPair(pair), "/admin/users");
    });
    await releaseUntilQuiet(server);

    server.revokeSession(viewedTokens);
    await act(async () => {
      void queryClient.invalidateQueries();
    });
    // Release waves until the admin restore's account read is in flight.
    const refusedAt = () =>
      server.requests.find(
        (request) => request.operation === "POST /api/v2/auth/refresh" && request.status === 401,
      );
    const restoreRead = () =>
      server.requests.find(
        (request) =>
          request.operation === "GET /api/v2/account/me" &&
          refusedAt() !== undefined &&
          request.seq > refusedAt()!.seq,
      );
    for (let wave = 0; wave < 20 && !restoreRead(); wave += 1) {
      await settle(server);
      if (!restoreRead()) server.releaseWave();
    }
    expect(restoreRead(), describeRequests(server.requests)).toBeDefined();

    // Another sign-in lands before the restore answers.
    const next = server.issueTokens();
    await act(async () => {
      setAccessToken(next.access_token);
      storage.set(storage.KEYS.REFRESH_TOKEN, next.refresh_token);
    });
    await releaseUntilQuiet(server);

    const log = describeRequests(server.requests);
    // The new session may rotate its own token; the admin's must not replace it.
    expect(storage.get(storage.KEYS.REFRESH_TOKEN), log).not.toBe(adminRefreshToken);
    expect(server.refreshTokensUsed, log).not.toContain(adminRefreshToken);
    expect(appRouter!.state.location.pathname, log).not.toBe("/login");
  });

  it("reads the viewed account once when an admin starts viewing as another user", async () => {
    signInReturningOwner();
    // The viewed account has two profiles, so its profile picker stays up
    // instead of auto-selecting one and clearing the cache again.
    let viewing = false;
    const viewedProfiles = {
      ...listProfilesOk,
      items: [
        ...listProfilesOk.items,
        { ...ownerProfile, id: "p-second", name: "Second", is_primary: false },
      ],
    };
    server = createFakeServer({
      ...serverRoutes(),
      "GET /api/v2/profiles": () => ({ body: viewing ? viewedProfiles : listProfilesOk }),
    });
    vi.stubGlobal("fetch", server.fetch);
    await boot(server);
    expect(screen.getByTestId("home"), describeRequests(server.requests)).toHaveTextContent(
      "2 sections",
    );
    const bootRequests = server.requests.length;

    // The picker is a lazy route chunk; load it now so the fake clock does not
    // outrun the module import.
    await import("@/pages/Profiles");

    // What AdminUserImpersonationDialog does with the impersonate answer.
    viewing = true;
    const pair = { ...adminAccountImpersonate, ...server.issueTokens() } as TokenPair;
    await act(async () => {
      homeAuth!.beginImpersonation(sessionFromTokenPair(pair), "/admin/users");
      await appRouter!.navigate("/profiles");
    });
    await releaseUntilQuiet(server);

    const viewedAccount = server.requests.slice(bootRequests);
    const log = describeRequests(viewedAccount);
    expect(screen.getByText("Second"), log).toBeInTheDocument();
    // The old account's cache is dropped before the new account renders, so
    // none of the new account's cached reads is thrown away and sent again.
    // The one repeated GET is /profiles: AuthProvider's sole-profile check
    // reads the list directly, beside the picker's cached read.
    expect(measureBoot(viewedAccount), log).toEqual({
      requestsBeforeHomeLayout: -1,
      wavesBeforeHomeLayout: -1,
      unauthorized: 0,
      refreshes: 0,
      duplicateGets: 1,
      total: 9,
    });
    expect(
      viewedAccount.filter((request) => request.operation === "GET /api/v2/profiles"),
      log,
    ).toHaveLength(2);
  });

  it("sends no account reads from the login screen", async () => {
    initialEntry = "/login";

    await boot(server);

    const log = describeRequests(server.requests);
    expect(measureBoot(server.requests), log).toEqual({
      requestsBeforeHomeLayout: -1,
      wavesBeforeHomeLayout: -1,
      unauthorized: 0,
      refreshes: 0,
      duplicateGets: 0,
      // Includes the login page's public signup-status and password-reset
      // capability reads.
      total: 6,
    });
  });
});
