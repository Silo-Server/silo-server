// @vitest-environment jsdom
import { afterEach, expect, it, vi } from "vitest";
import { cleanup, waitFor } from "@testing-library/react";
import { renderLogin, signInWithPassword } from "./loginPage.test-support";

const auth = vi.hoisted(() => ({
  login: vi.fn(),
  providers: [] as Array<{ id: string; mode: string; display_name: string; default?: boolean }>,
}));
vi.mock("@/hooks/useAuth", () => ({
  useAuth: () => ({
    loading: false,
    setupLoading: false,
    setupRequired: false,
    user: null,
    providers: auth.providers,
    login: auth.login,
  }),
  getBootstrapProfile: vi.fn(),
}));
vi.mock("@/api/v2/request", async () => ({
  ...(await vi.importActual<typeof import("@/api/v2/request")>("@/api/v2/request")),
  v2: vi.fn(async () => ({ enabled: false })),
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
afterEach(() => {
  cleanup();
  auth.login.mockReset();
});

const LOCAL = { id: "local", mode: "credentials", display_name: "Local", default: true };
const LDAP = { id: "plugin:6:ldap", mode: "credentials", display_name: "Directory" };

async function submit() {
  renderLogin();
  signInWithPassword("alice");
  await waitFor(() => expect(auth.login).toHaveBeenCalled());
  return auth.login.mock.calls[0];
}

it("lets the server route by account when a directory provider is listed", async () => {
  auth.providers = [LOCAL, LDAP];
  expect(await submit()).toEqual(["alice", "pw", undefined]);
});

it("routes by account while local sign-in is off and only the directory is listed", async () => {
  // Sending the directory's id would pass a break-glass admin's local password to it.
  auth.providers = [LDAP];
  expect(await submit()).toEqual(["alice", "pw", undefined]);
});

it("keeps the local provider when it is the only one", async () => {
  auth.providers = [LOCAL];
  expect(await submit()).toEqual(["alice", "pw", "local"]);
});
