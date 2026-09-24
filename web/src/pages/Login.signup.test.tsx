// @vitest-environment jsdom
import { afterEach, expect, it, vi } from "vitest";
import { act, cleanup, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import Login from "./Login";

const request = vi.hoisted(() => vi.fn());
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
    providers: [],
  }),
  getBootstrapProfile: vi.fn(),
}));
vi.mock("@/hooks/useServerBranding", () => ({
  useServerBranding: () => ({ serverName: "Silo", loginSubtitle: "Sign in" }),
}));
vi.mock("@/hooks/useDocumentTitle", () => ({ useDocumentTitle: vi.fn() }));
vi.mock("@/components/auth/AuthBackground", () => ({ AuthBackground: () => null }));
afterEach(() => {
  cleanup();
  request.mockReset();
});

function renderLogin() {
  render(
    <QueryClientProvider client={new QueryClient()}>
      <MemoryRouter initialEntries={["/login"]}>
        <Login />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

it("links to sign up when the server reports public signups enabled", async () => {
  request.mockResolvedValue({ enabled: true });
  renderLogin();
  expect(await screen.findByRole("link", { name: "Sign up" })).toBeTruthy();
  expect(request).toHaveBeenCalledWith("GET /api/v2/auth/signup");
});

it("hides the sign up link when public signups are disabled", async () => {
  request.mockResolvedValue({ enabled: false });
  renderLogin();
  await act(async () => {});
  expect(request).toHaveBeenCalledWith("GET /api/v2/auth/signup");
  expect(screen.queryByRole("link", { name: "Sign up" })).toBeNull();
});

it("hides the sign up link until the signup status loads", () => {
  request.mockReturnValue(new Promise(() => {}));
  renderLogin();
  expect(screen.queryByRole("link", { name: "Sign up" })).toBeNull();
});
