import { act, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { getProfileToken, setAccessToken, setProfileToken } from "@/api/client";
import type { Profile } from "@/api/types";
import { queryClient } from "@/lib/query-client";
import { getProfileLaunchMode, setProfileLaunchMode } from "@/lib/profileLaunch";
import { storage } from "@/utils/storage";
import { AuthProvider, useAuth } from "./useAuth";

// The real session client runs underneath, so the stored profile, its PIN
// proof, and the refresh token are the browser's actual state.

const account = { id: "1", username: "laura", email: "", role: "user", permissions: [] };

function v2Profile(id: string, hasPin: boolean) {
  return {
    id,
    name: id,
    has_pin: hasPin,
    is_child: false,
    is_primary: id === "parent",
    allowed_library_ids: [],
  };
}

const parent = { id: "parent", name: "parent", has_pin: true } as Profile;

function createServer(profiles: ReturnType<typeof v2Profile>[]) {
  const operations: string[] = [];
  const fetchImpl = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const raw = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
    const url = new URL(raw, "http://silo.test");
    const operation = `${(init?.method ?? "GET").toUpperCase()} ${url.pathname}`;
    operations.push(operation);
    const json = (body: unknown) =>
      new Response(JSON.stringify(body), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    switch (operation) {
      case "GET /api/v2/system/setup":
        return json({ needs_setup: false, wizard_completed: true });
      case "GET /api/v2/auth/providers":
        return json({ items: [] });
      case "POST /api/v2/auth/refresh":
        return json({ access_token: "access", refresh_token: "refresh-2", expires_in: 3600 });
      case "GET /api/v2/account/me":
        return json(account);
      case "GET /api/v2/profiles":
        return json({ items: profiles, avatar_upload_enabled: false });
      default:
        return new Response(null, { status: 404 });
    }
  }) as unknown as typeof fetch;
  return { fetch: fetchImpl, operations };
}

function Probe() {
  const { user, profile, loading, selectProfile, clearProfile, logout } = useAuth();
  return (
    <>
      <div data-testid="auth">
        {`${loading ? "restoring" : "restored"}:${user?.username ?? "none"}:${profile?.id ?? "none"}`}
      </div>
      <button type="button" onClick={() => selectProfile(parent, "pin-proof")}>
        pick parent
      </button>
      <button type="button" onClick={() => selectProfile(kid)}>
        pick kid
      </button>
      <button type="button" onClick={clearProfile}>
        switch profile
      </button>
      <button type="button" onClick={logout}>
        sign out
      </button>
    </>
  );
}

const kid = { id: "kid", name: "kid", has_pin: false } as Profile;

function launch() {
  return render(
    <MemoryRouter>
      <AuthProvider>
        <Probe />
      </AuthProvider>
    </MemoryRouter>,
  );
}

async function restored() {
  await waitFor(() => expect(screen.getByTestId("auth")).toHaveTextContent(/^restored:laura:/));
}

function click(name: string) {
  act(() => screen.getByRole("button", { name }).click());
}

/** The browser as an earlier tab left it: signed in, with a PIN profile remembered. */
function rememberParentProfile() {
  localStorage.setItem(storage.KEYS.PROFILE_ID, parent.id);
  localStorage.setItem(storage.KEYS.CURRENT_PROFILE, JSON.stringify(parent));
  localStorage.setItem(storage.KEYS.PROFILE_TOKEN, "pin-proof");
}

/**
 * Tabs share localStorage and each has its own sessionStorage. A tab is
 * simulated by swapping sessionStorage contents in and out.
 */
function saveTab(): Record<string, string> {
  const tab: Record<string, string> = {};
  for (let i = 0; i < sessionStorage.length; i++) {
    const key = sessionStorage.key(i) as string;
    tab[key] = sessionStorage.getItem(key) as string;
  }
  return tab;
}

function enterTab(tab: Record<string, string>) {
  sessionStorage.clear();
  for (const [key, value] of Object.entries(tab)) sessionStorage.setItem(key, value);
  // A loading tab reads its PIN proof into memory from its own storage, as
  // the client does at module start. Writing it back leaves it unchanged.
  setAccessToken(null);
  setProfileToken(storage.get(storage.KEYS.PROFILE_TOKEN));
}

describe("AuthProvider profile at launch", () => {
  let server: ReturnType<typeof createServer>;

  beforeEach(() => {
    localStorage.clear();
    sessionStorage.clear();
    queryClient.clear();
    setAccessToken(null);
    setProfileToken(null);
    sessionStorage.clear();
    server = createServer([v2Profile("parent", true), v2Profile("kid", false)]);
    vi.stubGlobal("fetch", server.fetch);
    localStorage.setItem(storage.KEYS.REFRESH_TOKEN, "refresh");
  });

  afterEach(() => {
    queryClient.clear();
    setAccessToken(null);
    setProfileToken(null);
    vi.unstubAllGlobals();
  });

  it("reopens the last profile with its PIN proof by default", async () => {
    rememberParentProfile();
    setProfileToken("pin-proof");

    launch();
    await restored();

    expect(screen.getByTestId("auth")).toHaveTextContent("restored:laura:parent");
    expect(getProfileToken()).toBe("pin-proof");
    expect(localStorage.getItem(storage.KEYS.PROFILE_TOKEN)).toBe("pin-proof");
  });

  it("asks who is watching in a new tab without a PIN proof, keeping the sign-in", async () => {
    setProfileLaunchMode("ask");
    rememberParentProfile();

    launch();
    await restored();

    expect(screen.getByTestId("auth")).toHaveTextContent("restored:laura:none");
    // Choosing the PIN profile needs its PIN: this tab has no proof.
    expect(storage.get(storage.KEYS.PROFILE_ID)).toBeNull();
    expect(storage.get(storage.KEYS.PROFILE_TOKEN)).toBeNull();
    expect(localStorage.getItem(storage.KEYS.REFRESH_TOKEN)).toBe("refresh-2");
  });

  it("keeps the profile chosen in this tab across a reload", async () => {
    setProfileLaunchMode("ask");

    const first = launch();
    await restored();
    click("pick parent");
    first.unmount();
    enterTab(saveTab());

    launch();
    await restored();

    expect(screen.getByTestId("auth")).toHaveTextContent("restored:laura:parent");
    expect(getProfileToken()).toBe("pin-proof");
  });

  it("asks again on reload while nobody has picked yet", async () => {
    setProfileLaunchMode("ask");
    const first = launch();
    await restored();
    first.unmount();
    enterTab(saveTab());

    launch();
    await restored();

    expect(screen.getByTestId("auth")).toHaveTextContent("restored:laura:none");
  });

  it("leaves the profile of an open tab alone when another tab launches", async () => {
    setProfileLaunchMode("ask");
    const a = launch();
    await restored();
    click("pick parent");
    a.unmount();
    const tabA = saveTab();

    enterTab({});
    const b = launch();
    await restored();
    expect(screen.getByTestId("auth")).toHaveTextContent("restored:laura:none");
    expect(getProfileToken()).toBeNull();
    click("pick kid");
    click("switch profile");
    b.unmount();

    enterTab(tabA);
    expect(storage.get(storage.KEYS.PROFILE_ID)).toBe("parent");
    expect(storage.get(storage.KEYS.PROFILE_TOKEN)).toBe("pin-proof");
    launch();
    await restored();
    expect(screen.getByTestId("auth")).toHaveTextContent("restored:laura:parent");
  });

  it("signing out in one tab clears the profile in every tab", async () => {
    setProfileLaunchMode("ask");
    const a = launch();
    await restored();
    click("pick parent");
    a.unmount();
    const tabA = saveTab();

    // Tab B opened before the switch to "ask" shares the remembered profile.
    enterTab({ "silo-profile-scope": "shared" });
    rememberParentProfile();
    const b = launch();
    await restored();
    expect(screen.getByTestId("auth")).toHaveTextContent("restored:laura:parent");
    click("sign out");
    await waitFor(() => expect(screen.getByTestId("auth")).toHaveTextContent(":none:none"));
    b.unmount();

    expect(localStorage.getItem(storage.KEYS.PROFILE_ID)).toBeNull();
    expect(localStorage.getItem(storage.KEYS.PROFILE_TOKEN)).toBeNull();
    enterTab(tabA);
    expect(storage.get(storage.KEYS.PROFILE_ID)).toBeNull();
    expect(storage.get(storage.KEYS.PROFILE_TOKEN)).toBeNull();
    expect(storage.get(storage.KEYS.CURRENT_PROFILE)).toBeNull();
  });

  it("switching to ask keeps the current tab's profile; switching back remembers it", async () => {
    const first = launch();
    await restored();
    click("pick parent");
    setProfileLaunchMode("ask");
    first.unmount();
    enterTab(saveTab());

    // The setting applies to new tabs: this one reloads into its profile.
    const reloaded = launch();
    await restored();
    expect(screen.getByTestId("auth")).toHaveTextContent("restored:laura:parent");
    reloaded.unmount();

    // A new tab picks the kid profile, then switches back to remembering.
    enterTab({});
    const tab = launch();
    await restored();
    click("pick kid");
    setProfileLaunchMode("remember");
    expect(getProfileLaunchMode()).toBe("remember");
    expect(localStorage.getItem(storage.KEYS.PROFILE_ID)).toBe("kid");
    expect(localStorage.getItem(storage.KEYS.PROFILE_TOKEN)).toBeNull();
    tab.unmount();

    enterTab({});
    launch();
    await restored();
    expect(screen.getByTestId("auth")).toHaveTextContent("restored:laura:kid");
  });

  it("shows the picker even for a lone unlocked profile when asked", async () => {
    vi.unstubAllGlobals();
    server = createServer([v2Profile("solo", false)]);
    vi.stubGlobal("fetch", server.fetch);
    setProfileLaunchMode("ask");

    launch();
    await restored();
    await act(async () => {});

    expect(screen.getByTestId("auth")).toHaveTextContent("restored:laura:none");
    expect(server.operations).not.toContain("GET /api/v2/profiles");
  });

  it("still selects a lone unlocked profile by itself when remembering", async () => {
    vi.unstubAllGlobals();
    server = createServer([v2Profile("solo", false)]);
    vi.stubGlobal("fetch", server.fetch);

    launch();

    await waitFor(() =>
      expect(screen.getByTestId("auth")).toHaveTextContent("restored:laura:solo"),
    );
  });
});
