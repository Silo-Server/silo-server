// @vitest-environment jsdom
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import Login from "./Login";

const request = vi.hoisted(() => vi.fn());
const providers = vi.hoisted(() => ({ list: [] as unknown[] }));
vi.mock("@/api/v2/request", async () => ({
  ...(await vi.importActual<typeof import("@/api/v2/request")>("@/api/v2/request")),
  v2: request,
}));
vi.mock("@/hooks/useAuth", () => ({
  useAuth: () => ({
    loading: false,
    setupLoading: false,
    setupRequired: false,
    user: null,
    providers: providers.list,
  }),
  getBootstrapProfile: vi.fn(),
}));
vi.mock("@/hooks/useServerBranding", () => ({
  useServerBranding: () => ({ serverName: "Silo", loginSubtitle: "Sign in" }),
}));
vi.mock("@/hooks/useDocumentTitle", () => ({ useDocumentTitle: vi.fn() }));
vi.mock("@/components/auth/AuthBackground", () => ({ AuthBackground: () => null }));

let resetState: string;
beforeEach(() => {
  resetState = "available";
  providers.list = [];
  request.mockImplementation(async (operation: string) =>
    operation === "GET /api/v2/capabilities/password-reset"
      ? { revision: "r", state: resetState }
      : { enabled: false },
  );
});
afterEach(() => {
  cleanup();
  request.mockReset();
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
  return client;
}

it("offers Forgot password? when the server offers self-service reset", async () => {
  renderLogin();
  const link = await screen.findByRole("link", { name: "Forgot password?" });
  expect(link.getAttribute("href")).toBe("/forgot-password");
  expect(request).toHaveBeenCalledWith(
    "GET /api/v2/capabilities/password-reset",
    expect.objectContaining({ retryAuthentication: false }),
  );
});

it("carries the typed username to the reset request", async () => {
  renderLogin();
  const link = await screen.findByRole("link", { name: "Forgot password?" });
  fireEvent.change(screen.getByLabelText("Username"), {
    target: { value: " alice@example.test " },
  });
  expect(link.getAttribute("href")).toBe("/forgot-password?login=alice%40example.test");
});

it.each(["disabled", "not_configured"])(
  "hides Forgot password? when reset is %s",
  async (state) => {
    resetState = state;
    const client = renderLogin();
    await waitFor(() =>
      expect(client.getQueryState(["auth", "password-reset-capability"])?.status).toBe("success"),
    );
    expect(screen.queryByRole("link", { name: "Forgot password?" })).toBeNull();
  },
);

it("hides Forgot password? while an external credential provider is selected", async () => {
  providers.list = [
    { id: "ldap", display_name: "Directory", mode: "credentials", default: true },
    { id: "local", display_name: "Silo", mode: "credentials", default: false },
  ];
  const client = renderLogin();
  await waitFor(() =>
    expect(client.getQueryState(["auth", "password-reset-capability"])?.status).toBe("success"),
  );
  expect(screen.queryByRole("link", { name: "Forgot password?" })).toBeNull();
});
