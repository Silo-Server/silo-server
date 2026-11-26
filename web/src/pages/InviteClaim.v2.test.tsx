// @vitest-environment jsdom
import { act, cleanup, fireEvent, screen, waitFor } from "@testing-library/react";
import { useNavigate } from "react-router";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setRefreshToken } from "@/api/client";
import {
  bodiesSentTo,
  json,
  openLink,
  problemResponse,
  renderRoutes,
} from "./authPage.test-support";
import InviteClaim from "./InviteClaim";
import { expectNoLinkCarrying } from "@/components/appKeys/codeLinkGuard.test-support";
import accepted from "../../../contracts/api/v2/fixtures/invitation_accepted.json";
import policyFixture from "../../../contracts/api/v2/fixtures/get_password_policy_ok.json";
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
  email_required: false,
  note: "",
  password_join: true,
  app_join: false,
};
const issuedCode = {
  code: "2t5oNw5THx0tyCc3SnhX0kz2PYJup7MIdr0Mmx9ps0o",
  expires_in: 600,
  server_address: "https://media.example.com",
  link_allowed: true,
};
const linkInvitation = { ...invitation, email: "", email_required: true, note: "For Sam" };
const emailProblem = (type: string, status: number, errors?: unknown[]) =>
  problemResponse(status, type, { errors });
function posts() {
  return fetchMock.mock.calls.filter(([url]) => String(url).endsWith("/api/v2/invitations/accept"));
}
function problem(status: number) {
  return new Response(
    JSON.stringify({ type: "https://silo.example/problems/failure", title: "Failure", status }),
    { status, headers: { "Content-Type": "application/problem+json" } },
  );
}
let submit: () => Promise<Response>;
let appCode: () => Promise<Response>;
let lookup: () => Promise<Response>;
const fetchMock = vi.fn<typeof fetch>();
function Move() {
  const navigate = useNavigate();
  return <button onClick={() => navigate("/login")}>Leave invitation</button>;
}
function mount(link = "/invite#first-secret") {
  openLink(link);
  return renderRoutes(
    "/invite",
    {
      "/invite": <InviteClaim />,
      "/household-setup": <p>Household setup</p>,
      "/login": <p>Ordinary login</p>,
    },
    <Move />,
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
  appCode = async () => json(issuedCode);
  fetchMock.mockImplementation(async (url) => {
    if (String(url).endsWith("/accept")) return submit();
    if (String(url).endsWith("/app-code")) return appCode();
    if (String(url).endsWith("/capabilities"))
      return json({ revision: "1", state: "available", default_profile: true, profileless: true });
    if (String(url).endsWith("/auth/password-policy")) return json(policyFixture);
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
  expect(screen.getByLabelText("Email")).toBeDisabled();
  send();
  await screen.findByText("Household setup");
  expect(bodiesSentTo(fetchMock, "/api/v2/invitations/lookup")).toEqual([
    { token: "first-secret" },
  ]);
  expect(JSON.parse(String(posts()[0]![1]!.body))).toEqual({
    token: "first-secret",
    password: "password123",
  });
  // The token travels only in request bodies, and the address no longer has it.
  expect(fetchMock.mock.calls.some(([url]) => String(url).includes("first-secret"))).toBe(false);
  expect(window.location.href).not.toContain("first-secret");
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
  expect(posts()).toHaveLength(1);
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
  expect(posts()).toHaveLength(1);
  expect(screen.getByLabelText("Password", { exact: true })).toHaveValue("password123");
  fireEvent.click(screen.getByRole("button", { name: "Reload invitation" }));
  await screen.findByLabelText("Email");
  expect(screen.getByRole("button", { name: "Create account" })).not.toBeDisabled();
  expect(fetchMock.mock.calls.some(([url]) => String(url).includes("/auth/refresh"))).toBe(false);
});
it("explains a refusal because password sign-in is off instead of offering recovery", async () => {
  submit = async () =>
    problemResponse(403, "local_login_disabled", { title: "Local password sign-in is turned off" });
  mount();
  await fill();
  send();
  await screen.findByText(/Password sign-in is turned off on this server/);
  expect(screen.getByText(/Ask Admin to add you/)).toBeTruthy();
  expect(screen.queryByText(/We could not confirm the result/)).toBeNull();
  expect(screen.getByRole("button", { name: "Create account" })).toBeDisabled();
  expect(auth.completeLogin).not.toHaveBeenCalled();
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
  expect(posts()).toHaveLength(0);
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
    await waitFor(() => expect(posts()).toHaveLength(1));
    if (change === "account") {
      setAccessToken("other-account");
      setAccessToken(null);
    } else fireEvent.click(screen.getByRole("button", { name: "Leave invitation" }));
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
  expect(posts()).toHaveLength(0);
  expect(auth.completeLogin).not.toHaveBeenCalled();
});
it("asks for an address on a link invitation and sends it with the password", async () => {
  lookup = async () => json(linkInvitation);
  mount();
  const email = await screen.findByLabelText("Email");
  expect(email).not.toBeDisabled();
  expect(screen.getByText("Note from Admin")).toBeInTheDocument();
  expect(screen.getByText("For Sam")).toBeInTheDocument();
  await fill();
  send();
  expect(
    screen.getByText("Enter a valid email address, like name@example.com"),
  ).toBeInTheDocument();
  expect(posts()).toHaveLength(0);
  fireEvent.change(email, { target: { value: "sam@example.test" } });
  send();
  await screen.findByText("Household setup");
  expect(JSON.parse(String(posts()[0]![1]!.body))).toEqual({
    token: "first-secret",
    email: "sam@example.test",
    password: "password123",
  });
});
it("treats a link that lost its token as unavailable without asking the server", async () => {
  mount("/invite");
  await screen.findByText("Invitation unavailable");
  expect(fetchMock).not.toHaveBeenCalled();
});
it("keeps a link invitation usable when the address already has an account", async () => {
  lookup = async () => json(linkInvitation);
  submit = async () => emailProblem("conflict", 409);
  mount();
  await fill();
  fireEvent.change(screen.getByLabelText("Email"), { target: { value: "taken@example.test" } });
  send();
  await screen.findByText(/An account already uses this email address/);
  expect(screen.queryByText(/We could not confirm the result/)).toBeNull();
  submit = async () =>
    emailProblem("validation_failed", 422, [
      { location: "body.email", code: "invalid", detail: "x" },
    ]);
  fireEvent.change(screen.getByLabelText("Email"), { target: { value: "other@example.test" } });
  send();
  await screen.findByText("Enter a valid email address, like name@example.com");
  expect(posts()).toHaveLength(2);
  expect(auth.completeLogin).not.toHaveBeenCalled();
});

// None of these refusals creates an account or spends the invitation, so the
// page says why and keeps Create account available (#4161).
it.each([
  { status: 429, type: "rate_limited", message: /Too many requests from this network/ },
  { status: 501, type: "capability_unsupported", message: /cannot create the profile required/ },
  { status: 409, type: "conflict", message: /An account already uses this email address/ },
  // A policy refusal: the field detail names the rule, not the summary.
  { status: 422, type: "validation_failed", message: /Include at least one capital letter/ },
])("lets the invitee retry after a $status $type refusal", async ({ status, type, message }) => {
  submit = async () =>
    problemResponse(status, type, {
      detail: "The request did not pass validation; see errors.",
      errors: [
        {
          location: "body.password",
          code: "invalid",
          detail: "Include at least one capital letter.",
        },
      ],
    });
  mount();
  await fill();
  send();
  await screen.findByText(message);
  expect(screen.queryByText(/We could not confirm the result/)).toBeNull();
  expect(screen.getByRole("button", { name: "Create account" })).not.toBeDisabled();

  submit = async () => json(accepted, 201);
  send();
  await screen.findByText("Household setup");
  expect(posts()).toHaveLength(2);
});

it("still treats a server error as an unconfirmed result", async () => {
  submit = async () => problem(500);
  mount();
  await fill();
  send();
  await screen.findByText(/We could not confirm the result/);
  expect(screen.getByRole("button", { name: "Create account" })).toBeDisabled();
});

it("joins a link invitation through the Silo app without creating anything first", async () => {
  lookup = async () => json({ ...linkInvitation, app_join: true });
  mount();
  fireEvent.click(await screen.findByRole("button", { name: "Join with the Silo app" }));
  fireEvent.change(screen.getByLabelText("Username"), { target: { value: " sam " } });
  fireEvent.click(screen.getByRole("button", { name: "Continue" }));
  const open = await screen.findByRole("link", { name: "Open Silo" });
  expect(open.getAttribute("href")).toBe(
    "silo://join#v1.aHR0cHM6Ly9tZWRpYS5leGFtcGxlLmNvbQ.2t5oNw5THx0tyCc3SnhX0kz2PYJup7MIdr0Mmx9ps0o",
  );
  // A join code keeps both forms: the custom scheme and the https link.
  expect(
    screen
      .getByRole("link", { name: "Open with the siloserver.org link instead" })
      .getAttribute("href"),
  ).toBe(
    "https://siloserver.org/join#v1.aHR0cHM6Ly9tZWRpYS5leGFtcGxlLmNvbQ.2t5oNw5THx0tyCc3SnhX0kz2PYJup7MIdr0Mmx9ps0o",
  );
  expect(bodiesSentTo(fetchMock, "/api/v2/invitations/app-code")).toEqual([
    { token: "first-secret", username: "sam" },
  ]);
  expect(posts()).toHaveLength(0);
  expect(auth.completeLogin).not.toHaveBeenCalled();
});

it("offers only the app where passwords can't claim, and says when the server refuses it", async () => {
  lookup = async () => json({ ...invitation, password_join: false, app_join: true });
  appCode = async () => problemResponse(409, "provider_sign_in_required");
  mount();
  fireEvent.click(await screen.findByRole("button", { name: "Continue" }));
  expect(await screen.findByRole("alert")).toHaveTextContent(
    "Test server adds people through its sign-in provider.",
  );
  expect(screen.queryByRole("button", { name: "Use a password instead" })).toBeNull();
  expect(screen.queryByLabelText("Password", { exact: true })).toBeNull();
});

it("renews a code once it expires", async () => {
  lookup = async () => json({ ...invitation, app_join: true });
  let issued = 0;
  appCode = async () =>
    json({
      ...issuedCode,
      code: `${++issued}`.padEnd(43, "A"),
      expires_in: issued === 1 ? 1 : 600,
    });
  mount();
  fireEvent.click(await screen.findByRole("button", { name: "Join with the Silo app" }));
  fireEvent.click(screen.getByRole("button", { name: "Continue" }));
  await screen.findByRole("link", { name: "Open Silo" });
  fireEvent.click(await screen.findByRole("button", { name: "Get a new code" }, { timeout: 3000 }));
  await screen.findByRole("link", { name: "Open Silo" });
  expect(bodiesSentTo(fetchMock, "/api/v2/invitations/app-code")).toHaveLength(2);
});

it("explains an invitation no path can claim instead of showing a form", async () => {
  lookup = async () => json({ ...invitation, password_join: false, app_join: false });
  mount();
  expect(await screen.findByRole("alert")).toHaveTextContent("Password sign-in is turned off");
  expect(screen.queryByRole("button", { name: "Create account" })).toBeNull();
});

it("puts a taken email on the email field and moves focus there", async () => {
  lookup = async () => json({ ...linkInvitation, app_join: true });
  appCode = async () =>
    problemResponse(409, "conflict", {
      errors: [{ location: "body.email", code: "duplicate", detail: "taken" }],
    });
  mount();
  fireEvent.click(await screen.findByRole("button", { name: "Join with the Silo app" }));
  const username = screen.getByLabelText("Username");
  expect(document.activeElement).toBe(username);
  fireEvent.change(username, { target: { value: "sam" } });
  fireEvent.change(screen.getByLabelText("Email (optional)"), {
    target: { value: "sam@example.test" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Continue" }));
  const email = screen.getByLabelText("Email (optional)");
  await waitFor(() => expect(document.activeElement).toBe(email));
  // Read with the field it describes, not also announced as an alert.
  expect(screen.queryByRole("alert")).toBeNull();
  expect(email).toHaveAccessibleDescription(/An account already uses this email address\./);
});

it("moves focus to the Open Silo step when the code arrives", async () => {
  lookup = async () => json({ ...invitation, app_join: true });
  mount();
  fireEvent.click(await screen.findByRole("button", { name: "Join with the Silo app" }));
  fireEvent.click(screen.getByRole("button", { name: "Continue" }));
  const heading = await screen.findByRole("heading", { name: "Open Silo" });
  await waitFor(() => expect(document.activeElement).toBe(heading));
});

it("moves focus to the renewal button when the code expires, and keeps a refused renewal on the step", async () => {
  lookup = async () => json({ ...invitation, app_join: true });
  let issued = 0;
  appCode = async () =>
    ++issued === 1 ? json({ ...issuedCode, expires_in: 1 }) : problemResponse(429, "rate_limited");
  mount();
  fireEvent.click(await screen.findByRole("button", { name: "Join with the Silo app" }));
  fireEvent.click(screen.getByRole("button", { name: "Continue" }));
  await screen.findByRole("link", { name: "Open Silo" });
  const renew = await screen.findByRole("button", { name: "Get a new code" }, { timeout: 3000 });
  await waitFor(() => expect(document.activeElement).toBe(renew));
  fireEvent.click(renew);
  expect(await screen.findByRole("alert")).toHaveTextContent("Too many requests");
  expect(screen.getByRole("heading", { name: "Open Silo" })).toBeTruthy();
  await waitFor(() =>
    expect(document.activeElement).toBe(screen.getByRole("button", { name: "Get a new code" })),
  );
});

it("returns to the form when a renewal finds the name taken", async () => {
  lookup = async () => json({ ...linkInvitation, app_join: true });
  let issued = 0;
  appCode = async () =>
    ++issued === 1
      ? json({ ...issuedCode, expires_in: 1 })
      : problemResponse(409, "conflict", {
          errors: [{ location: "body.username", code: "duplicate", detail: "taken" }],
        });
  mount();
  fireEvent.click(await screen.findByRole("button", { name: "Join with the Silo app" }));
  fireEvent.change(screen.getByLabelText("Username"), { target: { value: "sam" } });
  fireEvent.click(screen.getByRole("button", { name: "Continue" }));
  fireEvent.click(await screen.findByRole("button", { name: "Get a new code" }, { timeout: 3000 }));
  const username = await screen.findByLabelText("Username");
  await waitFor(() => expect(document.activeElement).toBe(username));
  expect(username).toHaveAccessibleDescription("That username is already in use. Choose another.");
});

it("says an emailed invitation's address is taken when its code is refused", async () => {
  lookup = async () => json({ ...invitation, app_join: true });
  appCode = async () =>
    problemResponse(409, "conflict", {
      errors: [{ location: "body.username", code: "duplicate", detail: "taken" }],
    });
  mount();
  fireEvent.click(await screen.findByRole("button", { name: "Join with the Silo app" }));
  fireEvent.click(screen.getByRole("button", { name: "Continue" }));
  expect(await screen.findByRole("alert")).toHaveTextContent(
    "An account already uses this email address.",
  );
});

// Focus moves once, when an answer comes back, never while the person types:
// a taken email, then a refusal with no field, then typing in the email field
// keeps focus there.
it("keeps focus where the person types after a later refusal", async () => {
  lookup = async () => json({ ...linkInvitation, app_join: true });
  let issued = 0;
  appCode = async () =>
    ++issued === 1
      ? problemResponse(409, "conflict", {
          errors: [{ location: "body.email", code: "duplicate", detail: "taken" }],
        })
      : problemResponse(429, "rate_limited");
  mount();
  fireEvent.click(await screen.findByRole("button", { name: "Join with the Silo app" }));
  fireEvent.change(screen.getByLabelText("Username"), { target: { value: "sam" } });
  const email = screen.getByLabelText("Email (optional)");
  fireEvent.change(email, { target: { value: "sam@example.test" } });
  fireEvent.click(screen.getByRole("button", { name: "Continue" }));
  await waitFor(() => expect(document.activeElement).toBe(email));
  fireEvent.click(screen.getByRole("button", { name: "Continue" }));
  expect(await screen.findByRole("alert")).toHaveTextContent("Too many requests");
  // The submit cleared the email's error; the refusal sent focus to the form.
  await waitFor(() => expect(document.activeElement).toBe(screen.getByLabelText("Username")));
  expect(email).not.toHaveAccessibleDescription(/already uses/);
  email.focus();
  fireEvent.change(email, { target: { value: "sam2@example.test" } });
  expect(document.activeElement).toBe(email);
});

// An error on the field that already has focus can't be read by moving
// focus there, so the status region says it.
it("announces an error on the field that has focus", async () => {
  lookup = async () => json({ ...linkInvitation, app_join: true });
  mount();
  fireEvent.click(await screen.findByRole("button", { name: "Join with the Silo app" }));
  const username = screen.getByLabelText("Username");
  expect(document.activeElement).toBe(username);
  fireEvent.submit(username.closest("form")!);
  expect(await screen.findByRole("status")).toHaveTextContent("Choose a username.");
  expect(username).toHaveAccessibleDescription("Choose a username.");
});

// A renewal from the Open Silo step finds the name taken: the form comes
// back with focus on the username field, which reads its error once; the
// status region stays empty.
it("reads a name taken at renewal once, from the username field", async () => {
  lookup = async () => json({ ...linkInvitation, app_join: true });
  let issued = 0;
  appCode = async () =>
    ++issued === 1
      ? json({ ...issuedCode, expires_in: 1 })
      : problemResponse(409, "conflict", {
          errors: [{ location: "body.username", code: "duplicate", detail: "taken" }],
        });
  mount();
  fireEvent.click(await screen.findByRole("button", { name: "Join with the Silo app" }));
  fireEvent.change(screen.getByLabelText("Username"), { target: { value: "sam" } });
  fireEvent.click(screen.getByRole("button", { name: "Continue" }));
  const renew = await screen.findByRole("button", { name: "Get a new code" }, { timeout: 3000 });
  renew.focus();
  fireEvent.click(renew);
  const username = await screen.findByLabelText("Username");
  await waitFor(() => expect(document.activeElement).toBe(username));
  expect(username).toHaveAccessibleDescription("That username is already in use. Choose another.");
  expect(screen.getByRole("status")).toHaveTextContent("");
});

// An administrator's invitation: the server says its code may not go in a
// link (an app that took the link would hold the new administrator's
// session), so the page shows it to type, with no link of any kind.
it("shows a code the server keeps out of links as text to type", async () => {
  lookup = async () => json({ ...invitation, app_join: true });
  appCode = async () => json({ ...issuedCode, link_allowed: false });
  const { container } = mount();
  fireEvent.click(await screen.findByRole("button", { name: "Join with the Silo app" }));
  fireEvent.click(screen.getByRole("button", { name: "Continue" }));
  expect(await screen.findByRole("heading", { name: "Join with this code" })).toBeTruthy();
  expect(screen.getByText(issuedCode.code)).toBeTruthy();
  expect(screen.queryByRole("link", { name: "Open Silo" })).toBeNull();
  expectNoLinkCarrying(container, issuedCode.code);
});

// A server from before link_allowed (two versions running during a deploy)
// leaves the member out. That means no link: the code is shown to type, not
// refused as a broken answer.
it("shows a code to type when the answer has no link_allowed", async () => {
  lookup = async () => json({ ...invitation, app_join: true });
  const { link_allowed: _dropped, ...withoutMember } = issuedCode;
  appCode = async () => json(withoutMember);
  const { container } = mount();
  fireEvent.click(await screen.findByRole("button", { name: "Join with the Silo app" }));
  fireEvent.click(screen.getByRole("button", { name: "Continue" }));
  expect(await screen.findByRole("heading", { name: "Join with this code" })).toBeTruthy();
  expect(screen.getByText(issuedCode.code)).toBeTruthy();
  expectNoLinkCarrying(container, issuedCode.code);
});
