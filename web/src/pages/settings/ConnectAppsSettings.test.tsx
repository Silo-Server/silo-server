// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { CompatConnectInfo, Profile } from "@/api/types";
import type { AccountIdentity } from "@/hooks/queries/account";
import type { AuthProviderOption } from "@/hooks/useAuth";

const mocks = vi.hoisted(() => ({
  useProfiles: vi.fn(),
  useAuth: vi.fn(),
  useCompatConnectInfo: vi.fn(),
  useAccountIdentities: vi.fn(),
  useAuthProviders: vi.fn(),
  copyTextToClipboard: vi.fn(),
}));

vi.mock("@/hooks/queries/profiles", () => ({
  useProfiles: (...args: unknown[]) => mocks.useProfiles(...args),
}));

vi.mock("@/hooks/useAuth", () => ({
  useAuth: (...args: unknown[]) => mocks.useAuth(...args),
}));

vi.mock("@/hooks/queries/compat", () => ({
  useCompatConnectInfo: (...args: unknown[]) => mocks.useCompatConnectInfo(...args),
}));

vi.mock("@/hooks/queries/account", () => ({
  useAccountIdentities: (...args: unknown[]) => mocks.useAccountIdentities(...args),
}));

vi.mock("@/hooks/queries/authProviders", () => ({
  useAuthProviders: (...args: unknown[]) => mocks.useAuthProviders(...args),
}));

vi.mock("@/lib/clipboard", () => ({
  copyTextToClipboard: (...args: unknown[]) => mocks.copyTextToClipboard(...args),
}));

import ConnectAppsSettings from "./ConnectAppsSettings";

function makeProfile(overrides: Partial<Profile> = {}): Profile {
  return {
    id: "profile-1",
    name: "Doe Household",
    avatar: "",
    has_pin: false,
    is_child: false,
    is_primary: true,
    max_content_rating: "",
    quality_preference: "auto",
    language: "en",
    subtitle_language: "",
    subtitle_mode: "auto",
    show_forced_subtitles: true,
    auto_skip_intro: false,
    auto_skip_credits: false,
    library_restrictions_enabled: false,
    allowed_library_ids: null,
    max_playback_quality: "",
    created_at: "2026-04-06T00:00:00Z",
    updated_at: "2026-04-06T00:00:00Z",
    ...overrides,
  };
}

function makeConnectInfo(
  overrides: Partial<CompatConnectInfo["jellyfin"]> = {},
  accountOverrides: Partial<CompatConnectInfo["account"]> = {},
): CompatConnectInfo {
  return {
    jellyfin: {
      enabled: true,
      pending_restart: false,
      public_url: "https://compat.example.test",
      server_name: "Example",
      ...overrides,
    },
    account: {
      password_login_available: true,
      ...accountOverrides,
    },
  };
}

const LOCAL_PROVIDER: AuthProviderOption = {
  id: "local",
  display_name: "Silo account",
  mode: "credentials",
  default: true,
};

const OIDC_PROVIDER: AuthProviderOption = {
  id: "plugin:3:oidc",
  display_name: "Company SSO",
  mode: "oauth",
  default: false,
  installation_id: "3",
  native_start_path: "/api/v2/auth/oauth/3/native/start",
};

const LDAP_PROVIDER: AuthProviderOption = {
  id: "plugin:4:ldap",
  display_name: "Office Directory",
  mode: "credentials",
  default: false,
  installation_id: "4",
};

function makeIdentity(provider: AuthProviderOption, username = "jdoe"): AccountIdentity {
  return {
    id: "7",
    installation_id: provider.installation_id ?? "0",
    provider_id: provider.id,
    provider_name: provider.display_name,
    username,
    email: "johndoe@example.test",
    display_name: "John Doe",
    linked_at: "2026-04-06T00:00:00.000Z",
    last_sign_in_at: null,
    last_checked_at: null,
  };
}

function findButton(container: HTMLElement, label: string) {
  return Array.from(container.querySelectorAll("button")).find((button) =>
    button.textContent?.trim().includes(label),
  );
}

async function click(element: Element | undefined) {
  if (!element) {
    throw new Error("element not found");
  }
  await act(async () => {
    element.dispatchEvent(new MouseEvent("click", { bubbles: true }));
  });
}

describe("ConnectAppsSettings", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    (
      globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT?: boolean }
    ).IS_REACT_ACT_ENVIRONMENT = true;
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);

    mocks.useProfiles.mockReset();
    mocks.useAuth.mockReset();
    mocks.useCompatConnectInfo.mockReset();
    mocks.copyTextToClipboard.mockReset();
    mocks.copyTextToClipboard.mockResolvedValue(undefined);

    mocks.useProfiles.mockReturnValue({
      data: [makeProfile(), makeProfile({ id: "profile-2", name: "Kids", has_pin: true })],
      isLoading: false,
    });
    mocks.useAuth.mockReturnValue({
      user: { username: "johndoe" },
      profile: { id: "profile-1" },
    });
    mocks.useCompatConnectInfo.mockReturnValue({
      data: makeConnectInfo(),
      isLoading: false,
    });
    mocks.useAccountIdentities.mockReset();
    mocks.useAccountIdentities.mockReturnValue({
      data: { items: [], can_unlink: false },
      isLoading: false,
    });
    mocks.useAuthProviders.mockReset();
    mocks.useAuthProviders.mockReturnValue({
      data: { items: [LOCAL_PROVIDER], password_login: true },
      isLoading: false,
    });
  });

  // An account whose only way in is the server's OIDC provider, with password
  // sign-in turned off on the server (#2608).
  function signInWithOIDCOnly() {
    mocks.useCompatConnectInfo.mockReturnValue({
      data: makeConnectInfo({}, { password_login_available: false }),
      isLoading: false,
    });
    mocks.useAuthProviders.mockReturnValue({
      data: { items: [OIDC_PROVIDER], password_login: false },
      isLoading: false,
    });
    mocks.useAccountIdentities.mockReturnValue({
      data: { items: [makeIdentity(OIDC_PROVIDER)], can_unlink: false },
      isLoading: false,
    });
  }

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    vi.restoreAllMocks();
  });

  // The page opens on the Silo tab; most cases here are about the Jellyfin one.
  function render(tab: "silo" | "jellyfin" = "jellyfin") {
    act(() => {
      root.render(<ConnectAppsSettings />);
    });
    if (tab === "jellyfin") {
      act(() => {
        findButton(container, "Jellyfin-compatible app")?.click();
      });
    }
  }

  it("switches the username and password format when another profile is picked", async () => {
    render();

    expect(container.textContent).toContain("has no PIN — just your account password");

    await click(findButton(container, "Kids"));

    expect(container.textContent).toContain("johndoe#Kids");
    expect(container.textContent).toContain("Kids has a PIN, so append # and the PIN");
  });

  it("refuses to hand out a username a # in the profile name would break", async () => {
    mocks.useProfiles.mockReturnValue({
      data: [makeProfile(), makeProfile({ id: "profile-2", name: "Movie #2" })],
      isLoading: false,
    });

    render();

    expect(container.textContent).toContain("Every profile at a glance");
    expect(container.textContent).toContain("rename to use from a Jellyfin app");
    expect(container.textContent).not.toContain("johndoe#Movie #2");

    await click(findButton(container, "Movie #2"));

    expect(container.textContent).toContain("contains a #, which Jellyfin apps can't sign in with");
    // No copy button, because the displayed string would not authenticate.
    expect(
      Array.from(container.querySelectorAll("button")).find(
        (button) => button.getAttribute("aria-label") === "Copy Username",
      ),
    ).toBeUndefined();
  });

  it("reports a failed load instead of claiming compat is switched off", () => {
    mocks.useCompatConnectInfo.mockReturnValue({
      data: undefined,
      isLoading: false,
      isError: true,
    });

    render();

    expect(container.textContent).toContain("Couldn't load your sign-in details");
    expect(container.textContent).not.toContain("is turned off");
  });

  // A failed profiles fetch leaves an empty list; without the error state the
  // page would fall through and present the bare account name as sufficient.
  it("withholds credentials when the profile list fails to load", () => {
    mocks.useProfiles.mockReturnValue({ data: [], isLoading: false, isError: true });

    render();

    expect(container.textContent).toContain("Couldn't load your sign-in details");
    expect(container.textContent).not.toContain("Every profile at a glance");
  });

  it("tells SSO accounts the compat API can't accept them", () => {
    mocks.useCompatConnectInfo.mockReturnValue({
      data: makeConnectInfo({}, { password_login_available: false }),
      isLoading: false,
    });

    render();

    expect(container.textContent).toContain("This account can't sign in to a Jellyfin app");
    expect(container.textContent).not.toContain("johndoe#Doe Household");
  });

  it("flags a loopback compat address rather than offering it for copying", () => {
    mocks.useCompatConnectInfo.mockReturnValue({
      data: makeConnectInfo({ public_url: "http://127.0.0.1:8096" }),
      isLoading: false,
    });

    render();

    expect(container.textContent).toContain("only works on the server itself");
    expect(
      Array.from(container.querySelectorAll("button")).find(
        (button) => button.getAttribute("aria-label") === "Copy Server",
      ),
    ).toBeUndefined();
  });

  it("tells an OIDC-only account to sign in through its provider on the Silo tab", () => {
    signInWithOIDCOnly();

    render("silo");

    expect(container.textContent).toContain("Choose Company SSO on the sign-in screen");
    expect(container.textContent).toContain(`${window.location.origin}/activate`);
    expect(container.textContent).not.toContain("your password");
    expect(container.textContent).not.toContain("Your account password");
    expect(container.textContent).not.toContain("Don't add a # to either field here.");
    // The profile picker still follows sign-in, whatever the method.
    expect(container.textContent).toContain("profile picker");
  });

  it("asks a directory account for its directory username and password", () => {
    mocks.useCompatConnectInfo.mockReturnValue({
      data: makeConnectInfo({}, { password_login_available: false }),
      isLoading: false,
    });
    mocks.useAuthProviders.mockReturnValue({
      data: { items: [LDAP_PROVIDER], password_login: true },
      isLoading: false,
    });
    mocks.useAccountIdentities.mockReturnValue({
      data: { items: [makeIdentity(LDAP_PROVIDER, "j.doe")], can_unlink: false },
      isLoading: false,
    });

    render("silo");

    expect(container.textContent).toContain("j.doe");
    expect(container.textContent).toContain("Your Office Directory password");
    expect(container.textContent).not.toContain("Your account password");
  });

  it("withholds Silo sign-in details when the sign-in options fail to load", () => {
    mocks.useAuthProviders.mockReturnValue({ data: undefined, isLoading: false, isError: true });

    render("silo");

    expect(container.textContent).toContain("Couldn't load your sign-in details");
    expect(container.textContent).not.toContain("your password");
  });

  it("points a break-glass account without a provider to the recovery sign-in", () => {
    mocks.useAuthProviders.mockReturnValue({
      data: { items: [OIDC_PROVIDER], password_login: false },
      isLoading: false,
    });

    render("silo");

    expect(container.textContent).toContain(
      "Silo's phone apps don't offer this account a way to sign in",
    );
    expect(container.textContent).toContain(`${window.location.origin}/login?local=1`);
    expect(container.textContent).not.toContain("Your account password");
  });
});
