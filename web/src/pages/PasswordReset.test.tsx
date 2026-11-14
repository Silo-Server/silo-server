// @vitest-environment jsdom
import { cleanup, fireEvent, screen } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setRefreshToken } from "@/api/client";
import {
  bodiesSentTo,
  json,
  openLink,
  problemResponse,
  renderRoutes,
} from "./authPage.test-support";
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

const problem = (status: number, type = "failure", errors?: unknown[]) =>
  problemResponse(status, type, { detail: "Failed.", errors });
let submit: () => Promise<Response>;
let lookup: () => Promise<Response>;
let selfService: string;
const fetchMock = vi.fn<typeof fetch>();

function mount(link = "/reset-password#secret-token") {
  openLink(link);
  return renderRoutes("/reset-password", {
    "/reset-password": <PasswordReset />,
    "/profiles": <p>Profile picker</p>,
    "/login": <p>Ordinary login</p>,
    "/forgot-password": <p>Request a link</p>,
  });
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
const completions = () => bodiesSentTo(fetchMock, "/api/v2/password-resets/complete");
const lookups = () => bodiesSentTo(fetchMock, "/api/v2/password-resets/lookup");

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
    return options?.method === "POST" && !String(url).endsWith("/lookup") ? submit() : lookup();
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
  expect(lookups()).toEqual([{ token: "secret-token" }]);
  expect(completions()).toEqual([{ token: "secret-token", password: "new-password" }]);
  // The token travels only in request bodies: no URL the page asked for, and
  // not the address its requests name as their Referer, carries it.
  expect(fetchMock.mock.calls.some(([url]) => String(url).includes("secret-token"))).toBe(false);
  expect(window.location.href).not.toContain("secret-token");
});

it("sends a link that lost its token to the forgot-password page", async () => {
  mount("/reset-password");
  await screen.findByText("Request a link");
  expect(lookups()).toHaveLength(0);
});

it("checks the new password before spending the link", async () => {
  mount();
  await fill("new-password", "different-password");
  send();
  await screen.findByText("Passwords do not match.");
  await fill("short");
  send();
  await screen.findByText("Use at least 8 characters.");
  expect(completions()).toHaveLength(0);
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
  expect(completions()).toHaveLength(1);
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

it("asks a signed-in visitor to sign out before using the link", async () => {
  auth.user = { username: "someone-else" };
  mount();
  await screen.findByText(/signed in as someone-else/);
  fireEvent.click(screen.getByRole("button", { name: "Sign out" }));
  expect(auth.logout).toHaveBeenCalled();
  expect(fetchMock).not.toHaveBeenCalled();
});
