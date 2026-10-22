// @vitest-environment jsdom
import { cleanup, fireEvent, screen } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setRefreshToken } from "@/api/client";
import { json, problemResponse, renderBrowserRoutes } from "./authPage.test-support";
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

const routes = {
  "/reset-password": <PasswordReset />,
  "/reset-password/:token": <PasswordReset />,
  "/profiles": <p>Profile picker</p>,
  "/login": <p>Ordinary login</p>,
  "/forgot-password": <p>Request a link</p>,
};
/** Opens `entry`, the emailed link by default. */
function mount(entry = "/reset-password#secret-token") {
  return renderBrowserRoutes(entry, routes);
}
/** Reloads the current history entry, its state included. */
function reload() {
  return renderBrowserRoutes(undefined, routes);
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
const calls = (path: string) => fetchMock.mock.calls.filter(([url]) => String(url).includes(path));
const posts = () => calls("/password-resets/complete");
const bodyOf = ([, init]: Parameters<typeof fetch>) => JSON.parse(String(init?.body));

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
    if (String(url).includes("/password-resets/complete")) return submit();
    if (String(url).includes("/password-resets/lookup")) return lookup();
    throw new Error(`unexpected request ${options?.method} ${String(url)}`);
  });
  vi.stubGlobal("fetch", fetchMock);
});
afterEach(() => {
  cleanup();
  vi.clearAllMocks();
  vi.unstubAllGlobals();
  window.history.replaceState(null, "", "/");
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
  expect(bodyOf(calls("/password-resets/lookup")[0]!)).toEqual({ token: "secret-token" });
  expect(bodyOf(posts()[0]!)).toEqual({ token: "secret-token", password: "new-password" });
  for (const [url] of fetchMock.mock.calls) expect(String(url)).not.toContain("secret-token");
});

it("takes the token out of the address bar and keeps it across a reload", async () => {
  const before = window.history.length;
  const { unmount } = mount();
  await screen.findByText(new RegExp(`For ${lookupFixture.username}`));
  expect(window.location.pathname).toBe("/reset-password");
  expect(window.location.hash).toBe("");
  expect(window.history.length).toBe(before);

  unmount();
  reload();
  await screen.findByText(new RegExp(`For ${lookupFixture.username}`));
  expect(calls("/password-resets/lookup").map(bodyOf)).toEqual([
    { token: "secret-token" },
    { token: "secret-token" },
  ]);
});

it("opens a link that carries the token in its path", async () => {
  mount("/reset-password/path-token");
  await screen.findByText(new RegExp(`For ${lookupFixture.username}`));
  expect(window.location.pathname).toBe("/reset-password");
  expect(calls("/password-resets/lookup").map(bodyOf)).toEqual([{ token: "path-token" }]);
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

it("explains a link that arrived without its token", async () => {
  mount("/reset-password");
  await screen.findByText("Incomplete link");
  expect(calls("/password-resets/lookup")).toHaveLength(0);
});

it("asks a signed-in visitor to sign out before using the link", async () => {
  auth.user = { username: "someone-else" };
  mount();
  await screen.findByText(/signed in as someone-else/);
  fireEvent.click(screen.getByRole("button", { name: "Sign out" }));
  expect(auth.logout).toHaveBeenCalled();
  expect(fetchMock).not.toHaveBeenCalled();
});
