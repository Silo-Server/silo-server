// @vitest-environment jsdom
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { V2ProblemError } from "@/api/v2/request";
import Login from "./Login";

const request = vi.hoisted(() => vi.fn());
const leave = vi.hoisted(() => vi.fn());
const navigate = vi.hoisted(() => vi.fn(async () => undefined));
const auth = vi.hoisted(() => ({
  login: vi.fn(),
  completeLogin: vi.fn(),
  providers: [] as Array<Record<string, unknown>>,
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
    completeLogin: auth.completeLogin,
    sessionRestoreUnavailable: false,
    sessionRestoreProviderUnavailable: false,
    retrySessionRestore: vi.fn(),
  }),
  getBootstrapProfile: vi.fn(),
}));
vi.mock("@/hooks/usePostSignInNavigation", () => ({
  CHANGE_PASSWORD_PATH: "/change-password",
  usePostSignInNavigation: () => navigate,
}));
vi.mock("@/hooks/useServerBranding", () => ({
  useServerBranding: () => ({ serverName: "Silo", loginSubtitle: "Sign in" }),
}));
vi.mock("@/hooks/useDocumentTitle", () => ({ useDocumentTitle: vi.fn() }));
vi.mock("@/components/auth/AuthBackground", () => ({ AuthBackground: () => null }));

const LOCAL = { id: "local", mode: "credentials", display_name: "Silo account", default: true };
const OIDC = {
  id: "plugin:5:oidc",
  installation_id: "5",
  mode: "oauth",
  display_name: "Company SSO",
  default: false,
};
const TAILSCALE = {
  id: "plugin:7:tailscale",
  installation_id: "7",
  mode: "network",
  display_name: "Tailscale",
  default: false,
  network_sign_in_path: "/api/v2/auth/network/7/sign-in",
  network_identity: { display_name: "Alice Example", username: "alice@example.test" },
};
const PAIR = {
  access_token: "access",
  refresh_token: "refresh",
  expires_in: 3600,
  user: { id: "1", username: "alice", email: "alice@example.test", role: "user" },
};

beforeEach(() => {
  auth.providers = [];
  request.mockImplementation(async (operation: string) => {
    if (operation === "POST /api/v2/auth/network/{id}/sign-in") return PAIR;
    return operation === "GET /api/v2/capabilities/password-reset"
      ? { revision: "r", state: "available" }
      : { enabled: false };
  });
});
afterEach(() => {
  cleanup();
  request.mockReset();
  leave.mockReset();
  navigate.mockClear();
  auth.completeLogin.mockReset();
});

function renderLogin() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={["/login"]}>
        <Login />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

it("signs the device owner in with one press, without a password", async () => {
  auth.providers = [LOCAL, TAILSCALE];
  renderLogin();
  const button = screen.getByRole("button", { name: /Continue as Alice Example/ });
  expect(screen.getByText("via Tailscale")).toBeTruthy();
  fireEvent.click(button);
  await waitFor(() => expect(auth.completeLogin).toHaveBeenCalled());
  expect(request).toHaveBeenCalledWith(
    "POST /api/v2/auth/network/{id}/sign-in",
    expect.objectContaining({ path: { id: "7" }, body: {} }),
  );
  expect(auth.completeLogin.mock.calls[0]?.[0]).toMatchObject({
    access_token: "access",
    refresh_token: "refresh",
  });
  await waitFor(() => expect(navigate).toHaveBeenCalled());
  expect(auth.login).not.toHaveBeenCalled();
  // The password form stays for people who would rather use it.
  expect(screen.getByLabelText("Password")).toBeTruthy();
});

it("falls back to the provider name when the owner is unnamed", () => {
  auth.providers = [LOCAL, { ...TAILSCALE, network_identity: undefined }];
  renderLogin();
  expect(screen.getByRole("button", { name: "Continue with Tailscale" })).toBeTruthy();
});

it("shows no password form when the network provider is the only way in", () => {
  auth.providers = [TAILSCALE];
  renderLogin();
  expect(screen.getByRole("button", { name: /Continue as Alice Example/ })).toBeTruthy();
  expect(screen.queryByLabelText("Password")).toBeNull();
  expect(screen.queryByText("or")).toBeNull();
});

it("does not leave for the only OAuth provider while a network sign-in is offered", async () => {
  auth.providers = [OIDC, TAILSCALE];
  renderLogin();
  expect(screen.getByRole("button", { name: /Continue as Alice Example/ })).toBeTruthy();
  await waitFor(() =>
    expect(request.mock.calls.map(([operation]) => operation)).toContain(
      "GET /api/v2/capabilities/password-reset",
    ),
  );
  expect(leave).not.toHaveBeenCalled();
});

it.each([
  ["not_permitted", "Tailscale doesn't allow this device to sign in to this server."],
  ["network_identity_required", "Open this server at its Tailscale address to sign in this way."],
  [
    "email_in_use",
    "An account with your email already exists. Sign in with your password, then connect Tailscale under Settings → Sign-in.",
  ],
])("explains a %s refusal", async (problemType, text) => {
  auth.providers = [LOCAL, TAILSCALE];
  request.mockImplementation(async (operation: string) => {
    if (operation === "POST /api/v2/auth/network/{id}/sign-in") {
      throw new V2ProblemError("signInWithNetworkIdentity", {
        type: `https://siloserver.org/docs/api/v2/problems/${problemType}`,
        title: "Refused",
        status: problemType === "email_in_use" ? 409 : 403,
      } as never);
    }
    return { enabled: false };
  });
  renderLogin();
  fireEvent.click(screen.getByRole("button", { name: /Continue as Alice Example/ }));
  expect((await screen.findByRole("alert")).textContent).toBe(text);
  expect(auth.completeLogin).not.toHaveBeenCalled();
});
