// @vitest-environment jsdom
import { afterEach, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import Login from "./Login";
vi.mock("@/hooks/useAuth", () => ({
  useAuth: () => ({
    loading: false,
    setupLoading: false,
    setupRequired: false,
    user: null,
    providers: [
      { id: "oauth-fixture", installation_id: 3, mode: "oauth", display_name: "Fixture provider" },
    ],
  }),
  getBootstrapProfile: vi.fn(),
}));
vi.mock("@/hooks/useServerBranding", () => ({
  useServerBranding: () => ({ serverName: "Silo", loginSubtitle: "Sign in" }),
}));
vi.mock("@/api/v2/request", async () => ({
  ...(await vi.importActual<typeof import("@/api/v2/request")>("@/api/v2/request")),
  v2: vi.fn(async () => ({ enabled: false })),
}));
vi.mock("@/hooks/useDocumentTitle", () => ({ useDocumentTitle: vi.fn() }));
vi.mock("@/components/auth/AuthBackground", () => ({ AuthBackground: () => null }));
afterEach(cleanup);
it("starts provider login with a top-level GET to v2 and preserves the encoded local destination", () => {
  render(
    <QueryClientProvider client={new QueryClient()}>
      <MemoryRouter initialEntries={["/login?redirect=%2Fme%3Ftab%3Dsettings"]}>
        <Login />
      </MemoryRouter>
    </QueryClientProvider>,
  );
  // Not a form post: the CSP's form-action 'self' would block the redirect to the provider.
  const link = screen.getByRole("link", { name: "Fixture provider" });
  expect(link.closest("form")).toBeNull();
  expect(link.getAttribute("href")).toBe("/api/v2/auth/oauth/3/start?next=%2Fme%3Ftab%3Dsettings");
});

it("tells a person without an account to ask an admin, apart from a provider refusal", () => {
  render(
    <QueryClientProvider client={new QueryClient()}>
      <MemoryRouter initialEntries={["/login?error=oauth_failed&reason=account_required"]}>
        <Login />
      </MemoryRouter>
    </QueryClientProvider>,
  );
  expect(
    screen.getByText("You don't have an account on this server yet. Ask an admin to add you."),
  ).toBeTruthy();
  expect(screen.queryByText(/isn't allowed to use this server/)).toBeNull();
});
