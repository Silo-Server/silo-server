// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useNavigate } from "react-router";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setRefreshToken } from "@/api/client";
import InviteClaim from "./InviteClaim";
import accepted from "../../../contracts/api/v2/fixtures/invitation_accepted.json";
const auth = vi.hoisted(() => ({ completeLogin: vi.fn() }));
vi.mock("@/hooks/useAuth", () => ({
  useAuth: () => ({ user: null, loading: false, completeLogin: auth.completeLogin }),
}));
vi.mock("@/components/auth/AuthBackground", () => ({ AuthBackground: () => null }));
const invitation = {
  email: "invitee@example.test",
  server_name: "Test server",
  inviter_name: "Admin",
  expires_at: "2026-10-01T00:00:00.000Z",
  show_tour: false,
  acceptance_available: true,
};
function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}
function problem(status: number) {
  return new Response(
    JSON.stringify({ type: "https://silo.example/problems/failure", title: "Failure", status }),
    { status, headers: { "Content-Type": "application/problem+json" } },
  );
}
let submit: () => Promise<Response>;
let lookup: () => Promise<Response>;
const fetchMock = vi.fn<typeof fetch>();
function Move() {
  const navigate = useNavigate();
  return <button onClick={() => navigate("/invite/second-secret")}>Change invitation</button>;
}
function mount() {
  return render(
    <MemoryRouter initialEntries={["/invite/first-secret"]}>
      <Move />
      <Routes>
        <Route path="/invite/:token" element={<InviteClaim />} />
        <Route path="/household-setup" element={<p>Household setup</p>} />
        <Route path="/login" element={<p>Ordinary login</p>} />
      </Routes>
    </MemoryRouter>,
  );
}
async function fill() {
  await screen.findByLabelText("Email");
  fireEvent.change(screen.getByLabelText("Password", { exact: true }), {
    target: { value: "password123" },
  });
  fireEvent.change(screen.getByLabelText("Confirm password"), { target: { value: "password123" } });
}
function send() {
  fireEvent.submit(screen.getByRole("button", { name: "Create account" }).closest("form")!);
}
beforeEach(() => {
  setAccessToken(null);
  setRefreshToken("existing-refresh");
  submit = async () => json(accepted, 201);
  lookup = async () => json(invitation);
  fetchMock.mockImplementation(async (url, options) => {
    if (options?.method === "POST") return submit();
    if (String(url).endsWith("/capabilities"))
      return json({ revision: "1", state: "available", default_profile: true, profileless: true });
    return lookup();
  });
  vi.stubGlobal("fetch", fetchMock);
});
afterEach(() => {
  cleanup();
  vi.clearAllMocks();
  vi.unstubAllGlobals();
  setRefreshToken(null);
});
it("installs the nested v2 session and continues household setup", async () => {
  mount();
  await fill();
  send();
  await screen.findByText("Household setup");
  expect(auth.completeLogin).toHaveBeenCalledWith(
    expect.objectContaining({
      access_token: accepted.tokens.access_token,
      user: expect.objectContaining({ id: Number(accepted.tokens.user.id) }),
    }),
  );
});
it("treats sign-in-required as committed, clears passwords, and offers ordinary login", async () => {
  submit = async () =>
    json({ status: "accepted", login_status: "sign_in_required", username: invitation.email }, 201);
  mount();
  await fill();
  send();
  await screen.findByText(/Your account was created/);
  expect(screen.queryByLabelText("Password", { exact: true })).toBeNull();
  expect(auth.completeLogin).not.toHaveBeenCalled();
  expect(fetchMock.mock.calls.filter(([, init]) => init?.method === "POST")).toHaveLength(1);
  fireEvent.click(screen.getAllByRole("link", { name: "Sign in" })[0]!);
  await screen.findByText("Ordinary login");
});
it("does not refresh or replay a 401 accept and requires explicit reload before another attempt", async () => {
  submit = async () => problem(401);
  mount();
  await fill();
  send();
  await screen.findByText(/We could not confirm the result/);
  send();
  expect(fetchMock.mock.calls.filter(([, init]) => init?.method === "POST")).toHaveLength(1);
  expect(screen.getByLabelText("Password", { exact: true })).toHaveValue("password123");
  fireEvent.click(screen.getByRole("button", { name: "Reload invitation" }));
  await screen.findByLabelText("Email");
  expect(screen.getByRole("button", { name: "Create account" })).not.toBeDisabled();
  expect(fetchMock.mock.calls.some(([url]) => String(url).includes("/auth/refresh"))).toBe(false);
});
it("does not confuse a storage failure with an unavailable invitation", async () => {
  lookup = async () => problem(500);
  mount();
  await screen.findByText("Could not load invitation");
  expect(screen.queryByText("Invitation unavailable")).toBeNull();
  lookup = async () => problem(404);
  fireEvent.click(screen.getByRole("button", { name: "Reload invitation" }));
  await screen.findByText("Invitation unavailable");
});
it("blocks unsupported per-invitation profile creation", async () => {
  lookup = async () => json({ ...invitation, acceptance_available: false });
  mount();
  await fill();
  send();
  expect(fetchMock.mock.calls.filter(([, init]) => init?.method === "POST")).toHaveLength(0);
});
it.each(["account", "route"])(
  "rejects delayed installation after a %s change and blocks double submit",
  async (change) => {
    let resolve!: (response: Response) => void;
    submit = () =>
      new Promise((done) => {
        resolve = done;
      });
    mount();
    await fill();
    const form = screen.getByRole("button", { name: "Create account" }).closest("form")!;
    fireEvent.submit(form);
    fireEvent.submit(form);
    await waitFor(() =>
      expect(fetchMock.mock.calls.filter(([, init]) => init?.method === "POST")).toHaveLength(1),
    );
    if (change === "account") {
      setAccessToken("other-account");
      setAccessToken(null);
    } else fireEvent.click(screen.getByRole("button", { name: "Change invitation" }));
    await act(async () => {
      resolve(json(accepted, 201));
    });
    expect(auth.completeLogin).not.toHaveBeenCalled();
    expect(screen.queryByText("Household setup")).toBeNull();
  },
);
it("rejects inconsistent partial acceptance without installing credentials", async () => {
  submit = async () => json({ ...accepted, login_status: "sign_in_required" }, 201);
  mount();
  await fill();
  send();
  await screen.findByText(/We could not confirm the result/);
  expect(auth.completeLogin).not.toHaveBeenCalled();
});

it("rejects a submit when credentials arrived before auth context rerender", async () => {
  mount();
  await fill();
  setAccessToken("new-account");
  send();
  expect(fetchMock.mock.calls.filter(([, init]) => init?.method === "POST")).toHaveLength(0);
  expect(auth.completeLogin).not.toHaveBeenCalled();
});
