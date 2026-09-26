// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter, Route, Routes } from "react-router";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setRefreshToken } from "@/api/client";
import PasswordReset from "./PasswordReset";
import completed from "../../../contracts/api/v2/fixtures/password_reset_completed.json";
import lookupFixture from "../../../contracts/api/v2/fixtures/password_reset_lookup.json";
import capabilityFixture from "../../../contracts/api/v2/fixtures/password_reset_capability.json";

const auth = vi.hoisted(() => ({
  user: null as null | { username: string },
  completeLogin: vi.fn(),
  logout: vi.fn(),
  selectProfile: vi.fn(),
}));
vi.mock("@/hooks/useAuth", () => ({
  useAuth: () => ({ ...auth, loading: false }),
  getBootstrapProfile: () => null,
}));
vi.mock("@/hooks/queries/profiles", () => ({
  listProfiles: async () => ({ profiles: [] }),
}));
vi.mock("@/components/auth/AuthBackground", () => ({ AuthBackground: () => null }));

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}
function problem(status: number, type = "failure", errors?: unknown[]) {
  return new Response(
    JSON.stringify({
      type: `https://siloserver.org/docs/api/v2/problems/${type}`,
      title: type,
      status,
      detail: "Failed.",
      errors,
    }),
    { status, headers: { "Content-Type": "application/problem+json" } },
  );
}
let submit: () => Promise<Response>;
let lookup: () => Promise<Response>;
let selfService: string;
const fetchMock = vi.fn<typeof fetch>();

function mount() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={["/reset-password/secret-token"]}>
        <Routes>
          <Route path="/reset-password/:token" element={<PasswordReset />} />
          <Route path="/profiles" element={<p>Profile picker</p>} />
          <Route path="/login" element={<p>Ordinary login</p>} />
          <Route path="/forgot-password" element={<p>Request a link</p>} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}
async function fill(password = "new-password", confirmation = password) {
  fireEvent.change(await screen.findByLabelText("New password"), { target: { value: password } });
  fireEvent.change(screen.getByLabelText("Confirm new password"), {
    target: { value: confirmation },
  });
}
function send() {
  fireEvent.submit(screen.getByRole("button", { name: "Save password" }).closest("form")!);
}
const posts = () => fetchMock.mock.calls.filter(([, init]) => init?.method === "POST");

beforeEach(() => {
  auth.user = null;
  setAccessToken(null);
  setRefreshToken(null);
  submit = async () => json(completed);
  lookup = async () => json(lookupFixture);
  selfService = "disabled";
  fetchMock.mockImplementation(async (url, options) => {
    if (String(url).includes("/capabilities/password-reset")) {
      return json({ ...capabilityFixture, state: selfService });
    }
    return options?.method === "POST" ? submit() : lookup();
  });
  vi.stubGlobal("fetch", fetchMock);
});
afterEach(() => {
  cleanup();
  vi.clearAllMocks();
  vi.unstubAllGlobals();
});

it("sets the password through the link, signs in, and continues to the profiles", async () => {
  mount();
  await screen.findByText(new RegExp(`For ${lookupFixture.username}`));
  await fill();
  send();
  await screen.findByText("Profile picker");
  expect(auth.completeLogin).toHaveBeenCalledWith(
    expect.objectContaining({ access_token: completed.tokens.access_token }),
  );
  const [url, init] = posts()[0]!;
  expect(String(url)).toContain("/api/v2/password-resets/secret-token/complete");
  expect(JSON.parse(String(init?.body))).toEqual({ password: "new-password" });
});

it("checks the new password before spending the link", async () => {
  mount();
  await fill("new-password", "different-password");
  send();
  await screen.findByText("Passwords do not match.");
  await fill("short");
  send();
  await screen.findByText("Use at least 8 characters.");
  expect(posts()).toHaveLength(0);
});

it("reports a committed reset whose sign-in failed without replaying it", async () => {
  submit = async () =>
    json({
      status: "completed",
      login_status: "sign_in_required",
      username: lookupFixture.username,
    });
  mount();
  await fill();
  send();
  await screen.findByText(new RegExp(`Sign in as ${lookupFixture.username}`));
  expect(auth.completeLogin).not.toHaveBeenCalled();
  expect(posts()).toHaveLength(1);
});

it("shows a field problem and keeps the link usable", async () => {
  submit = async () =>
    problem(422, "validation_failed", [
      { location: "body.password", code: "invalid", detail: "Use at most 72 bytes." },
    ]);
  mount();
  await fill();
  send();
  await screen.findByText("Use at most 72 bytes.");
  expect(screen.getByRole("button", { name: "Save password" })).not.toBeDisabled();
});

it("explains an unusable link", async () => {
  lookup = async () => problem(404, "not_found");
  mount();
  await screen.findByText("Link unavailable");
  expect(screen.queryByLabelText("New password")).toBeNull();
  await screen.findByText(/Ask your admin for a new link/);
  expect(screen.queryByRole("link", { name: "Request a new link" })).toBeNull();
});

it("offers a new link for an unusable one when self-service reset is on", async () => {
  selfService = "available";
  lookup = async () => problem(404, "not_found");
  mount();
  fireEvent.click(await screen.findByRole("link", { name: "Request a new link" }));
  expect(await screen.findByText("Request a link")).toBeTruthy();
});

it("asks a signed-in visitor to sign out before using the link", async () => {
  auth.user = { username: "someone-else" };
  mount();
  await screen.findByText(/signed in as someone-else/);
  fireEvent.click(screen.getByRole("button", { name: "Sign out" }));
  expect(auth.logout).toHaveBeenCalled();
  expect(fetchMock).not.toHaveBeenCalled();
});
