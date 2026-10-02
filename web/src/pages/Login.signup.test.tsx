// @vitest-environment jsdom
import { afterEach, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
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

const SIGNUP_STATUS = ["auth", "signup-status"];

function renderLogin(client = new QueryClient({ defaultOptions: { queries: { retry: false } } })) {
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={["/login"]}>
        <Login />
      </MemoryRouter>
    </QueryClientProvider>,
  );
  return client;
}

it("links to sign up when the server reports public signups enabled", async () => {
  request.mockResolvedValue({ enabled: true });
  renderLogin();
  expect(await screen.findByRole("link", { name: "Sign up" })).toBeTruthy();
  expect(request).toHaveBeenCalledWith("GET /api/v2/auth/signup");
});

it("hides the sign up link once the server reports signups disabled", async () => {
  request.mockResolvedValue({ enabled: false });
  const client = renderLogin();
  await waitFor(() => expect(client.getQueryState(SIGNUP_STATUS)?.status).toBe("success"));
  expect(client.getQueryData(SIGNUP_STATUS)).toEqual({ enabled: false });
  expect(screen.queryByRole("link", { name: "Sign up" })).toBeNull();
});

it("hides the sign up link until the signup status loads", () => {
  request.mockReturnValue(new Promise(() => {}));
  renderLogin();
  expect(screen.queryByRole("link", { name: "Sign up" })).toBeNull();
});

it("ignores a cached enabled status and uses the fresh one", async () => {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: 120_000 } },
  });
  client.setQueryData(SIGNUP_STATUS, { enabled: true });
  let finish: (value: { enabled: boolean }) => void = () => {};
  request.mockReturnValue(new Promise((resolve) => (finish = resolve)));
  renderLogin(client);

  expect(request).toHaveBeenCalledWith("GET /api/v2/auth/signup");
  expect(screen.queryByRole("link", { name: "Sign up" })).toBeNull();

  finish({ enabled: false });
  await waitFor(() => expect(client.getQueryData(SIGNUP_STATUS)).toEqual({ enabled: false }));
  expect(screen.queryByRole("link", { name: "Sign up" })).toBeNull();
});

it("hides the sign up link when a refetch fails", async () => {
  request.mockResolvedValueOnce({ enabled: true });
  const client = renderLogin();
  await screen.findByRole("link", { name: "Sign up" });

  request.mockRejectedValueOnce(new Error("offline"));
  await client.refetchQueries({ queryKey: SIGNUP_STATUS });
  await waitFor(() => expect(screen.queryByRole("link", { name: "Sign up" })).toBeNull());
});
