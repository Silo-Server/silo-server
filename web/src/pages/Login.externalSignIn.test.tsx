// @vitest-environment jsdom
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { cleanup, screen, waitFor } from "@testing-library/react";
import { v2Problem } from "@/api/v2/problems.test-support";
import { markSignedOut } from "@/lib/externalSignIn";
import {
  renderLogin,
  signInWithPassword,
  waitForPasswordResetCapability,
} from "./loginPage.test-support";

const request = vi.hoisted(() => vi.fn());
const leave = vi.hoisted(() => vi.fn());
const auth = vi.hoisted(() => ({
  login: vi.fn(),
  providers: [] as Array<Record<string, unknown>>,
  sessionRestoreUnavailable: false,
  sessionRestoreProviderUnavailable: false,
}));
vi.mock("@/api/v2/request", async () => ({
  ...(await vi.importActual<typeof import("@/api/v2/request")>("@/api/v2/request")),
  v2: request,
}));
vi.mock("@/lib/externalSignIn", async () => ({
  ...(await vi.importActual<typeof import("@/lib/externalSignIn")>("@/lib/externalSignIn")),
  leaveForProvider: leave,
}));
vi.mock("@/hooks/useAuth", () => ({
  useAuth: () => ({
    loading: false,
    setupLoading: false,
    setupRequired: false,
    user: null,
    providers: auth.providers,
    login: auth.login,
    sessionRestoreUnavailable: auth.sessionRestoreUnavailable,
    sessionRestoreProviderUnavailable: auth.sessionRestoreProviderUnavailable,
    retrySessionRestore: vi.fn(),
  }),
  getBootstrapProfile: vi.fn(),
}));
vi.mock("@/hooks/usePostSignInNavigation", () => ({
  CHANGE_PASSWORD_PATH: "/change-password",
  usePostSignInNavigation: () => vi.fn(async () => undefined),
}));
vi.mock("@/hooks/useServerBranding", () => ({
  useServerBranding: () => ({ serverName: "Silo", loginSubtitle: "Sign in" }),
}));
vi.mock("@/hooks/useDocumentTitle", () => ({ useDocumentTitle: vi.fn() }));
vi.mock("@/components/auth/AuthBackground", () => ({ AuthBackground: () => null }));

const LOCAL = { id: "local", mode: "credentials", display_name: "Silo account", default: true };
const LDAP = {
  id: "plugin:6:ldap",
  installation_id: "6",
  mode: "credentials",
  display_name: "Directory",
  default: false,
};
const OIDC = {
  id: "plugin:5:oidc",
  installation_id: "5",
  mode: "oauth",
  display_name: "Company SSO",
  icon_url: "/api/v2/plugins/5/icon.svg",
  default: false,
};

beforeEach(() => {
  window.sessionStorage.clear();
  auth.providers = [];
  auth.sessionRestoreUnavailable = false;
  auth.sessionRestoreProviderUnavailable = false;
  request.mockImplementation(async (operation: string) =>
    operation === "GET /api/v2/capabilities/password-reset"
      ? { revision: "r", state: "available" }
      : { enabled: false },
  );
});
afterEach(() => {
  cleanup();
  request.mockReset();
  leave.mockReset();
  auth.login.mockReset();
});

const settled = () => waitForPasswordResetCapability(request);

it.each([
  ["not_permitted", "Your account at the sign-in provider isn't allowed to use this server."],
  ["something_new", "Sign-in with the provider failed. Try again."],
])("explains the %s refusal and does not send the person straight back", async (reason, text) => {
  auth.providers = [OIDC];
  renderLogin(`/login?error=oauth_failed&reason=${reason}`);
  expect(screen.getByRole("alert").textContent).toBe(text);
  if (reason === "not_permitted") expect(screen.queryByText(/not_permitted/)).toBeNull();
  await settled();
  expect(leave).not.toHaveBeenCalled();
});

it("goes straight to the only provider when password sign-in is off", async () => {
  auth.providers = [OIDC];
  renderLogin("/login?redirect=%2Factivate%3Fcode%3D1");
  await waitFor(() =>
    expect(leave).toHaveBeenCalledWith("/api/v2/auth/oauth/5/start?next=%2Factivate%3Fcode%3D1"),
  );
  expect(leave).toHaveBeenCalledTimes(1);
  expect(screen.getByText("Taking you to Company SSO…")).toBeTruthy();
  expect(screen.queryByLabelText("Password")).toBeNull();
  expect(screen.queryByRole("link", { name: "Forgot password?" })).toBeNull();
});

it("shows the password form instead with /login?local=1", async () => {
  auth.providers = [OIDC];
  auth.login.mockResolvedValue({ id: "1" });
  renderLogin("/login?local=1");
  await screen.findByRole("link", { name: "Forgot password?" });
  expect(leave).not.toHaveBeenCalled();
  signInWithPassword("admin");
  // No provider named: the server keeps the name on the local provider.
  await waitFor(() => expect(auth.login).toHaveBeenCalledWith("admin", "pw", undefined));
});

it("does not go back to the provider after signing out in this tab", async () => {
  // The provider may still have its own session and would sign the person
  // straight back in.
  markSignedOut();
  auth.providers = [OIDC];
  renderLogin();
  await settled();
  expect(leave).not.toHaveBeenCalled();
  expect(screen.getByRole("link", { name: "Company SSO" })).toBeTruthy();
});

it("does not auto-redirect when a password provider or a second provider is listed", async () => {
  auth.providers = [LDAP, OIDC];
  renderLogin();
  await settled();
  expect(leave).not.toHaveBeenCalled();

  cleanup();
  auth.providers = [OIDC, { ...OIDC, id: "plugin:7:oidc", installation_id: "7" }];
  renderLogin();
  await settled();
  expect(leave).not.toHaveBeenCalled();
});

it("asks the provider to pick an account when switching account", async () => {
  auth.providers = [OIDC];
  renderLogin("/login?redirect=%2Factivate%3Fcode%3D48217730&switch_account=1");
  const link = screen.getByRole("link", { name: "Company SSO" });
  expect(link.getAttribute("href")).toBe(
    "/api/v2/auth/oauth/5/start?next=%2Factivate%3Fcode%3D48217730&prompt=select_account",
  );
  await settled();
  expect(leave).not.toHaveBeenCalled();
});

it("keeps Automatic routing with a directory provider and hides Forgot password there", async () => {
  auth.providers = [LOCAL, LDAP];
  renderLogin();
  await settled();
  expect(screen.getAllByText("Automatic").length).toBeGreaterThan(0);
  expect(screen.queryByRole("link", { name: "Forgot password?" })).toBeNull();
});

it.each([
  ["local_login_disabled", 403, [LDAP], "Password sign-in is turned off on this server."],
  [
    "password_expired",
    403,
    [LDAP, OIDC],
    "Your directory password has expired. Change it with your organization, then sign in again.",
  ],
  [
    "not_permitted",
    403,
    [LDAP, OIDC],
    "Your account at the sign-in provider isn't allowed to use this server.",
  ],
  // A disabled Silo account (v1's user_disabled).
  ["permission_denied", 403, [LOCAL], "This account is disabled."],
])("explains the %s password sign-in refusal", async (type, status, providers, text) => {
  auth.providers = providers;
  auth.login.mockRejectedValue(v2Problem(status, type, "raw detail"));
  renderLogin();
  signInWithPassword("alice");
  expect((await screen.findByRole("alert")).textContent).toBe(text);
  expect(screen.queryByText("raw detail")).toBeNull();
});

it("stays on the password form after a refusal on /login?local=1", async () => {
  // A refusal on /login?local=1 must not bounce the person to the provider.
  auth.providers = [OIDC];
  auth.login.mockRejectedValue(v2Problem(403, "local_login_disabled", "raw detail"));
  renderLogin("/login?local=1");
  signInWithPassword("alice");
  await screen.findByRole("alert");
  expect(leave).not.toHaveBeenCalled();
});

it.each([
  [
    true,
    "Can't reach the sign-in provider right now. You're still signed in; try again in a moment.",
  ],
  [false, "Can't restore your session right now. You're still signed in; try again in a moment."],
])(
  "names the sign-in provider in the restore banner only for its outage (provider outage %s)",
  async (providerOutage, text) => {
    auth.providers = [LOCAL, OIDC];
    auth.sessionRestoreUnavailable = true;
    auth.sessionRestoreProviderUnavailable = providerOutage;
    renderLogin();
    await settled();
    expect(screen.getByRole("alert").querySelector("p")?.textContent).toBe(text);
    // A session that may still work never bounces the person to the provider.
    expect(leave).not.toHaveBeenCalled();
  },
);
