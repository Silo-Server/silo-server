// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter, useLocation } from "react-router";
import { afterEach, beforeEach, expect, it, vi } from "vitest";

import { v2, V2ProblemError } from "@/api/v2/request";
import { AccountSignInSection } from "./AccountSignInSection";

const auth = vi.hoisted(() => ({
  providers: [] as Array<Record<string, unknown>>,
  isImpersonating: false,
}));
const leave = vi.hoisted(() => vi.fn());
const toastSuccess = vi.hoisted(() => vi.fn());

vi.mock("@/hooks/useAuth", () => ({ useAuth: () => auth }));
vi.mock("@/api/v2/request", async (original) => ({
  ...(await original<typeof import("@/api/v2/request")>()),
  v2: vi.fn(),
}));
vi.mock("@/lib/externalSignIn", async (original) => ({
  ...(await original<typeof import("@/lib/externalSignIn")>()),
  leaveForProvider: leave,
}));
vi.mock("sonner", () => ({ toast: { success: toastSuccess } }));

const LOCAL = { id: "local", mode: "credentials", display_name: "Silo account", default: true };
const OIDC = {
  id: "plugin:5:oidc",
  installation_id: "5",
  mode: "oauth",
  display_name: "Company SSO",
  default: false,
};
const LDAP = {
  id: "plugin:6:ldap",
  installation_id: "6",
  mode: "credentials",
  display_name: "Directory",
  default: false,
};
const TAILSCALE = {
  id: "plugin:7:tailscale",
  installation_id: "7",
  mode: "network",
  display_name: "Tailscale",
  default: false,
  network_identity: { display_name: "Alice Example", username: "alice@example.test" },
};
const IDENTITY = {
  id: "4",
  installation_id: "5",
  provider_id: "plugin:5:oidc",
  provider_name: "Company SSO",
  username: "alice",
  email: "alice@example.test",
  display_name: "Alice",
  linked_at: "2026-01-02T03:04:05.678Z",
  last_sign_in_at: new Date(Date.now() - 3 * 3_600_000).toISOString() as string | null,
  last_checked_at: new Date(Date.now() - 3_600_000).toISOString() as string | null,
};

let identities: Array<typeof IDENTITY>;
let passwordAllowed: boolean;
let failures: Record<string, unknown>;
const calls: Array<{ op: string; options?: unknown }> = [];

function problem(type: string, status: number, location?: string) {
  return new V2ProblemError("op", {
    type: `https://siloserver.org/docs/api/v2/problems/${type}`,
    title: type,
    status,
    detail: `raw ${type}`,
    ...(location ? { errors: [{ location, code: "invalid", detail: "x" }] } : {}),
  } as never);
}

beforeEach(() => {
  auth.providers = [LOCAL, OIDC];
  auth.isImpersonating = false;
  identities = [];
  passwordAllowed = true;
  failures = {};
  calls.length = 0;
  vi.mocked(v2).mockImplementation(((op: string, options?: unknown) => {
    calls.push({ op, options });
    if (failures[op]) return Promise.reject(failures[op]);
    switch (op) {
      case "GET /api/v2/account/identities":
        // The server's rule: another identity, or a password it still accepts.
        return Promise.resolve({
          items: identities,
          can_unlink: identities.length > 0 && (identities.length > 1 || passwordAllowed),
        });
      case "GET /api/v2/account/password/capability":
        return Promise.resolve({
          revision: "1",
          state: "available",
          allowed: passwordAllowed,
          requires_current_password: true,
          minimum_password_length: 8,
          maximum_password_bytes: 72,
        });
      case "POST /api/v2/account/identities/link-ticket":
        return Promise.resolve({ ticket: "t-1", expires_at: "2026-01-02T03:09:05.678Z" });
      case "POST /api/v2/account/identities/link-start":
        return Promise.resolve({ authorize_url: "https://id.example.test/authorize?state=s" });
      case "POST /api/v2/account/identities/link-credentials":
        identities = [{ ...IDENTITY, id: "9", installation_id: "6", provider_name: "Directory" }];
        return Promise.resolve(identities[0]);
      case "POST /api/v2/account/identities/link-network":
        identities = [{ ...IDENTITY, id: "10", installation_id: "7", provider_name: "Tailscale" }];
        return Promise.resolve(identities[0]);
      case "DELETE /api/v2/account/identities/{id}":
        identities = [];
        return Promise.resolve(undefined);
    }
    return Promise.reject(new Error(`unexpected ${op}`));
  }) as never);
});
afterEach(() => {
  cleanup();
  leave.mockReset();
  toastSuccess.mockReset();
});

function Where() {
  const location = useLocation();
  return <p data-testid="where">{location.pathname + location.search}</p>;
}

function mount(entry = "/settings/account") {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[entry]}>
        <AccountSignInSection />
        <Where />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

const opCalls = (op: string) => calls.filter((call) => call.op === op);

it("shows the linked identity and keeps it when it is the only way in", async () => {
  identities = [IDENTITY];
  passwordAllowed = false;
  mount();
  await screen.findByText("alice · alice@example.test");
  expect(screen.getByText("Company SSO")).toBeTruthy();
  expect(
    screen.getByText(/^Connected .+ · Last sign-in 3h ago · Last checked 1h ago$/),
  ).toBeTruthy();
  await screen.findByText("This is how you sign in to this account, so it stays connected.");
  expect(screen.queryByRole("button", { name: "Disconnect" })).toBeNull();
  // One provider, already linked: nothing to connect.
  expect(screen.queryByRole("button", { name: /Connect/ })).toBeNull();
});

it("disconnects while the local password still signs in", async () => {
  identities = [IDENTITY];
  mount();
  fireEvent.click(await screen.findByRole("button", { name: "Disconnect" }));
  const dialog = await screen.findByRole("alertdialog");
  expect(within(dialog).getByText("Disconnect Company SSO?")).toBeTruthy();
  fireEvent.click(within(dialog).getByRole("button", { name: "Disconnect" }));
  await waitFor(() => expect(toastSuccess).toHaveBeenCalledWith("Disconnected Company SSO"));
  expect(opCalls("DELETE /api/v2/account/identities/{id}")[0]?.options).toEqual({
    path: { id: "4" },
    profileContext: undefined,
    retryAuthentication: false,
  });
  await screen.findByRole("button", { name: "Connect Company SSO" });
});

it("explains the server's refusal to remove the last sign-in method", async () => {
  identities = [IDENTITY];
  failures["DELETE /api/v2/account/identities/{id}"] = problem("last_sign_in_method", 409);
  mount();
  fireEvent.click(await screen.findByRole("button", { name: "Disconnect" }));
  const dialog = await screen.findByRole("alertdialog");
  fireEvent.click(within(dialog).getByRole("button", { name: "Disconnect" }));
  expect((await within(dialog).findByRole("alert")).textContent).toBe(
    "Company SSO is the only way to sign in to your account, so it can't be disconnected. Ask an admin to set a Silo password for you first.",
  );
});

async function openOAuthConnect() {
  mount();
  fireEvent.click(await screen.findByRole("button", { name: "Connect Company SSO" }));
  fireEvent.change(screen.getByLabelText("Silo password"), { target: { value: "local pw" } });
  fireEvent.click(screen.getByRole("button", { name: "Continue to Company SSO" }));
}

it("connects an OAuth provider after the local password, in this browser", async () => {
  await openOAuthConnect();
  await waitFor(() =>
    expect(leave).toHaveBeenCalledWith("https://id.example.test/authorize?state=s"),
  );
  expect(opCalls("POST /api/v2/account/identities/link-ticket")[0]?.options).toEqual({
    body: { installation_id: "5", password: "local pw" },
    profileContext: undefined,
    retryAuthentication: false,
  });
  expect(opCalls("POST /api/v2/account/identities/link-start")[0]?.options).toEqual({
    body: { link_ticket: "t-1", next: "/settings/account" },
    profileContext: undefined,
    retryAuthentication: false,
  });
});

it.each([
  [
    "POST /api/v2/account/identities/link-ticket",
    problem("validation_failed", 422, "body.password"),
    "That isn't your current Silo password.",
  ],
  [
    "POST /api/v2/account/identities/link-ticket",
    problem("local_password_required", 409),
    "Your account has no Silo password to confirm this with. Ask an admin to connect the provider for you.",
  ],
  [
    "POST /api/v2/account/identities/link-start",
    problem("conflict", 409),
    "Open this server at its public address to connect a sign-in provider.",
  ],
  [
    "POST /api/v2/account/identities/link-start",
    problem("provider_unavailable", 503),
    "The sign-in provider can't be reached right now. Try again later.",
  ],
])("explains a refused OAuth connect (%s)", async (op, failure, text) => {
  failures[op] = failure;
  await openOAuthConnect();
  expect((await screen.findByRole("alert")).textContent).toBe(text);
  expect(leave).not.toHaveBeenCalled();
});

async function submitDirectoryConnect() {
  auth.providers = [LOCAL, LDAP];
  mount();
  fireEvent.click(await screen.findByRole("button", { name: "Connect Directory" }));
  fireEvent.change(screen.getByLabelText("Silo password"), { target: { value: "local pw" } });
  fireEvent.change(screen.getByLabelText("Directory username"), { target: { value: "alice" } });
  fireEvent.change(screen.getByLabelText("Directory password"), { target: { value: "dir pw" } });
  fireEvent.click(screen.getByRole("button", { name: "Connect" }));
}

it("connects a directory with its username and password", async () => {
  await submitDirectoryConnect();
  await screen.findByText("Connected Directory. Sign in with it from now on.");
  expect(opCalls("POST /api/v2/account/identities/link-credentials")[0]?.options).toEqual({
    body: {
      installation_id: "6",
      password: "local pw",
      username: "alice",
      directory_password: "dir pw",
    },
    profileContext: undefined,
    retryAuthentication: false,
  });
  // The list is read again and shows the new identity.
  await screen.findByText("alice · alice@example.test");
  expect(leave).not.toHaveBeenCalled();
});

it.each([
  [problem("validation_failed", 422, "body.password"), "That isn't your current Silo password."],
  [
    problem("validation_failed", 422, "body.directory_password"),
    "Directory didn't accept that username and password.",
  ],
  [problem("account_disabled", 403), "Your Directory account is disabled."],
  [
    problem("password_expired", 403),
    "Your Directory password has expired. Change it there, then try again.",
  ],
  [
    problem("not_permitted", 403),
    "Your account at the sign-in provider isn't allowed to use this server.",
  ],
  [
    problem("identity_linked_elsewhere", 409),
    "That provider account is already connected to another account on this server.",
  ],
  [problem("already_linked", 409), "Your account is already connected to this sign-in provider."],
  [
    problem("local_password_required", 409),
    "Your account has no Silo password to confirm this with. Ask an admin to connect the provider for you.",
  ],
])("explains a refused directory connect (%#)", async (failure, text) => {
  failures["POST /api/v2/account/identities/link-credentials"] = failure;
  await submitDirectoryConnect();
  expect((await screen.findByRole("alert")).textContent).toBe(text);
});

async function submitNetworkConnect() {
  auth.providers = [LOCAL, TAILSCALE];
  mount();
  fireEvent.click(await screen.findByRole("button", { name: "Connect Tailscale" }));
  expect(
    screen.getByText(/Confirm your Silo password to connect Alice Example's Tailscale account/),
  ).toBeTruthy();
  fireEvent.change(screen.getByLabelText("Silo password"), { target: { value: "local pw" } });
  fireEvent.click(screen.getByRole("button", { name: "Connect" }));
}

it("connects the network identity of this device with the Silo password only", async () => {
  await submitNetworkConnect();
  await screen.findByText("Connected Tailscale. Sign in with it from now on.");
  expect(opCalls("POST /api/v2/account/identities/link-network")[0]?.options).toEqual({
    body: { installation_id: "7", password: "local pw" },
    profileContext: undefined,
    retryAuthentication: false,
  });
  expect(leave).not.toHaveBeenCalled();
});

it.each([
  [
    problem("network_identity_required", 403),
    "Open this server at its Tailscale address to connect Tailscale.",
  ],
  [problem("validation_failed", 422, "body.password"), "That isn't your current Silo password."],
])("explains a refused network connect (%#)", async (failure, text) => {
  failures["POST /api/v2/account/identities/link-network"] = failure;
  await submitNetworkConnect();
  expect((await screen.findByRole("alert")).textContent).toBe(text);
});

it("shows the result a linking flow came back with, once", async () => {
  mount("/settings/account?linked=1");
  expect(
    screen.getByText("Your account is connected to the sign-in provider.").getAttribute("role"),
  ).toBe("status");
  await waitFor(() => expect(screen.getByTestId("where").textContent).toBe("/settings/account"));

  cleanup();
  mount("/settings/account?error=oauth_link_failed&reason=identity_linked_elsewhere");
  expect(screen.getByRole("alert").textContent).toBe(
    "That provider account is already connected to another account on this server.",
  );
  await waitFor(() => expect(screen.getByTestId("where").textContent).toBe("/settings/account"));

  cleanup();
  mount("/settings/account?error=oauth_link_failed&reason=state_invalid");
  expect(screen.getByRole("alert").textContent).toBe(
    "Connecting didn't finish in this browser. Try again from this page.",
  );
});

it("changes nothing while an admin views the account as another user", async () => {
  auth.isImpersonating = true;
  identities = [IDENTITY];
  auth.providers = [LOCAL, OIDC, LDAP];
  mount();
  await screen.findByText(/Only the account itself can change how it signs in/);
  expect(screen.queryByRole("button", { name: "Disconnect" })).toBeNull();
  expect(screen.queryByRole("button", { name: /Connect/ })).toBeNull();
});

it("stays hidden on a server without an external provider", async () => {
  auth.providers = [LOCAL];
  mount();
  await waitFor(() => expect(opCalls("GET /api/v2/account/identities")).toHaveLength(1));
  expect(screen.queryByText("Sign-in")).toBeNull();
});
