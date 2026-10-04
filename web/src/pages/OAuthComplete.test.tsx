// @vitest-environment jsdom
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import { V2ProblemError } from "@/api/v2/request";
import OAuthComplete from "./OAuthComplete";

const request = vi.hoisted(() => vi.fn());
const completeLogin = vi.hoisted(() => vi.fn());
const tokens = vi.hoisted(() => ({ access: vi.fn(), refresh: vi.fn() }));

vi.mock("@/api/v2/request", async () => ({
  ...(await vi.importActual<typeof import("@/api/v2/request")>("@/api/v2/request")),
  v2: request,
}));
vi.mock("@/api/client", () => ({
  setAccessToken: tokens.access,
  setRefreshToken: tokens.refresh,
}));
vi.mock("@/api/v2/account", () => ({
  userFromAccount: (account: { id: string; username: string }) => ({
    id: account.id,
    username: account.username,
  }),
}));
vi.mock("@/hooks/useAuth", () => ({ useAuth: () => ({ completeLogin }) }));

const COMPLETION = {
  access_token: "access",
  refresh_token: "refresh",
  expires_in: 900,
  next: "/activate?code=48217730",
};

function Where() {
  const location = useLocation();
  return <p data-testid="where">{location.pathname + location.search}</p>;
}

function mount() {
  render(
    <MemoryRouter initialEntries={["/login/oauth-complete"]}>
      <Routes>
        <Route path="/login/oauth-complete" element={<OAuthComplete />} />
        <Route path="*" element={<Where />} />
      </Routes>
    </MemoryRouter>,
  );
}

async function landedOn(path: string) {
  await waitFor(() => expect(screen.getByTestId("where").textContent).toBe(path));
}

beforeEach(() => {
  window.history.replaceState(null, "", "/login/oauth-complete?code=one-time");
});
afterEach(() => {
  cleanup();
  request.mockReset();
  completeLogin.mockReset();
  tokens.access.mockReset();
  tokens.refresh.mockReset();
  window.history.replaceState(null, "", "/");
});

it("redeems the code once and goes on to where the sign-in started", async () => {
  request.mockImplementation(async (operation: string) =>
    operation === "POST /api/v2/auth/oauth/complete" ? COMPLETION : { id: "1", username: "alice" },
  );
  mount();
  await landedOn("/activate?code=48217730");
  expect(request).toHaveBeenCalledWith("POST /api/v2/auth/oauth/complete", {
    body: { code: "one-time" },
  });
  expect(
    request.mock.calls.filter(([op]) => op === "POST /api/v2/auth/oauth/complete"),
  ).toHaveLength(1);
  // The single-use code leaves the address bar.
  expect(window.location.search).toBe("");
  expect(completeLogin).toHaveBeenCalledWith(
    expect.objectContaining({ access_token: "access", user: { id: "1", username: "alice" } }),
  );
});

it("sends a refused code back to the login page, which does not restart the provider", async () => {
  request.mockRejectedValue(
    new V2ProblemError("completeOAuthLogin", {
      type: "https://siloserver.org/docs/api/v2/problems/invalid_grant",
      title: "Invalid grant",
      status: 400,
      detail: "raw",
    } as never),
  );
  mount();
  await landedOn("/login?error=oauth_failed&reason=session_expired");
  expect(completeLogin).not.toHaveBeenCalled();
  expect(tokens.access).toHaveBeenLastCalledWith(null);
});

it("reports a failure after the code was redeemed as a failed sign-in", async () => {
  request.mockImplementation(async (operation: string) => {
    if (operation === "POST /api/v2/auth/oauth/complete") return COMPLETION;
    throw new V2ProblemError("getAccount", {
      type: "https://siloserver.org/docs/api/v2/problems/internal_error",
      title: "Internal error",
      status: 500,
    } as never);
  });
  mount();
  await landedOn("/login?error=oauth_failed&reason=login_failed");
  expect(completeLogin).not.toHaveBeenCalled();
  expect(tokens.refresh).toHaveBeenLastCalledWith(null);
});

it("sends a visit without a code back to the login page", async () => {
  window.history.replaceState(null, "", "/login/oauth-complete");
  mount();
  await landedOn("/login?error=oauth_failed&reason=state_invalid");
  expect(request).not.toHaveBeenCalled();
});
