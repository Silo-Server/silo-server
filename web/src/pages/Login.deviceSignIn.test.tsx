// @vitest-environment jsdom
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import Login from "./Login";

const request = vi.hoisted(() => vi.fn());
const leave = vi.hoisted(() => vi.fn());
const auth = vi.hoisted(() => ({
  providers: [] as Array<Record<string, unknown>>,
  sessionRestoreUnavailable: false,
  sessionRestoreProviderUnavailable: false,
  retrySessionRestore: vi.fn(),
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
    sessionRestoreUnavailable: auth.sessionRestoreUnavailable,
    sessionRestoreProviderUnavailable: auth.sessionRestoreProviderUnavailable,
    retrySessionRestore: auth.retrySessionRestore,
    login: vi.fn(),
    completeLogin: vi.fn(),
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

const START = {
  device_code: "dev-1",
  user_code: "4821-7730",
  match_code: "warm pony",
  verification_uri: "https://silo.example.test/activate",
  verification_uri_complete: "https://silo.example.test/activate?code=48217730",
  expires_in: 900,
  interval: 5,
};

let pollAnswer: Record<string, unknown>;

beforeEach(() => {
  window.sessionStorage.clear();
  auth.providers = [{ id: "local", mode: "credentials", display_name: "Silo", default: true }];
  auth.sessionRestoreUnavailable = false;
  auth.sessionRestoreProviderUnavailable = false;
  auth.retrySessionRestore.mockReset();
  pollAnswer = { status: "pending", opened: false };
  request.mockImplementation(async (operation: string) => {
    switch (operation) {
      case "POST /api/v2/auth/device/start":
        return START;
      case "POST /api/v2/auth/device/poll":
        return pollAnswer;
      case "POST /api/v2/auth/device/cancel":
        return { status: "canceled" };
      case "GET /api/v2/capabilities/password-reset":
        return { revision: "r", state: "available" };
    }
    return { enabled: false };
  });
});
afterEach(() => {
  cleanup();
  request.mockReset();
  leave.mockReset();
});

function renderLogin() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={["/login"]}>
        <Login />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

const cancels = () =>
  request.mock.calls.filter(([operation]) => operation === "POST /api/v2/auth/device/cancel");

async function showCode() {
  fireEvent.click(await screen.findByRole("button", { name: "Show QR code" }));
  await screen.findByText("4821 7730");
}

it("shows the grouped code and the address under the QR, with no match words", async () => {
  renderLogin();
  await showCode();
  expect(screen.getByText("4821 7730").getAttribute("aria-hidden")).toBe("true");
  expect(screen.getByText("4 8 2 1 7 7 3 0")).toBeTruthy();
  expect(screen.getByText("https://silo.example.test/activate")).toBeTruthy();
  expect(screen.queryByText(/Match code/i)).toBeNull();
  expect(screen.queryByText(/warm pony/i)).toBeNull();
});

it("tells the person to continue on the phone once the link was opened", async () => {
  pollAnswer = { status: "pending", opened: true };
  renderLogin();
  await showCode();
  expect(await screen.findByText("Continue on your phone.")).toBeTruthy();
});

it("withdraws the request on Start over", async () => {
  renderLogin();
  await showCode();
  fireEvent.click(screen.getByRole("button", { name: "Start over" }));
  await waitFor(() => expect(cancels()).toHaveLength(1));
  expect(cancels()[0]![1]).toEqual({ body: { device_code: "dev-1" } });
  expect(screen.getByRole("button", { name: "Show QR code" })).toBeTruthy();
});

it("withdraws a waiting request when the page goes away", async () => {
  const view = renderLogin();
  await showCode();
  view.unmount();
  await waitFor(() => expect(cancels()).toHaveLength(1));
});

it("does not withdraw a request that was declined", async () => {
  pollAnswer = { status: "denied" };
  const view = renderLogin();
  fireEvent.click(await screen.findByRole("button", { name: "Show QR code" }));
  await waitFor(() =>
    expect(request.mock.calls.some(([op]) => op === "POST /api/v2/auth/device/poll")).toBe(true),
  );
  // The declined request is gone from the page.
  await screen.findByRole("button", { name: "Show QR code" });
  view.unmount();
  expect(cancels()).toHaveLength(0);
});

it("names the provider picker and says what Automatic does", async () => {
  auth.providers = [
    { id: "local", mode: "credentials", display_name: "Silo", default: true },
    { id: "plugin:6:ldap", installation_id: "6", mode: "credentials", display_name: "Directory" },
  ];
  renderLogin();
  const picker = await screen.findByRole("combobox", { name: "Sign in with" });
  expect(picker).toHaveAccessibleDescription(/Automatic: accounts with a Silo password/);
});

it("keeps the session and offers a retry when the provider can't be reached", async () => {
  auth.providers = [
    { id: "plugin:5:oidc", installation_id: "5", mode: "oauth", display_name: "Company SSO" },
  ];
  // The refresh answered 503 provider_unavailable.
  auth.sessionRestoreUnavailable = true;
  auth.sessionRestoreProviderUnavailable = true;
  renderLogin();
  expect(await screen.findByText(/Can't reach the sign-in provider right now/)).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Try again" }));
  expect(auth.retrySessionRestore).toHaveBeenCalled();
  // The only provider is down: going there would not help.
  expect(leave).not.toHaveBeenCalled();
});
