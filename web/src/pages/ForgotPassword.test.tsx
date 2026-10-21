// @vitest-environment jsdom
import { cleanup, fireEvent, screen } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { json, problemResponse, renderRoutes } from "./authPage.test-support";
import ForgotPassword from "./ForgotPassword";
import capabilityFixture from "../../../contracts/api/v2/fixtures/password_reset_capability.json";

const auth = vi.hoisted(() => ({ user: null as null | { username: string } }));
vi.mock("@/hooks/useAuth", () => ({
  useAuth: () => ({ ...auth, pendingPasswordChange: null, loading: false }),
}));
vi.mock("@/hooks/useServerBranding", () => ({
  useServerBranding: () => ({ serverName: "Silo", loginSubtitle: "" }),
}));
vi.mock("@/components/auth/AuthBackground", () => ({ AuthBackground: () => null }));

const problem = (status: number, type: string) =>
  problemResponse(status, type, { detail: "Refused." });

let state: string;
let submit: () => Promise<Response>;
const fetchMock = vi.fn<typeof fetch>();

function mount(entry = "/forgot-password?login=alice") {
  return renderRoutes(entry, { "/forgot-password": <ForgotPassword />, "/": <p>Home</p> });
}
const posts = () => fetchMock.mock.calls.filter(([, init]) => init?.method === "POST");
function send() {
  fireEvent.submit(screen.getByRole("button", { name: "Send reset link" }).closest("form")!);
}

beforeEach(() => {
  auth.user = null;
  state = "available";
  submit = async () => new Response(null, { status: 202 });
  fetchMock.mockImplementation(async (_url, options) =>
    options?.method === "POST" ? submit() : json({ ...capabilityFixture, state }),
  );
  vi.stubGlobal("fetch", fetchMock);
});
afterEach(() => {
  cleanup();
  fetchMock.mockReset();
  vi.unstubAllGlobals();
});

it("sends the request and answers the same whether or not an account matched", async () => {
  mount();
  const field = await screen.findByLabelText("Username or email");
  expect((field as HTMLInputElement).value).toBe("alice");
  fireEvent.change(field, { target: { value: "  alice@example.test " } });
  send();
  expect((await screen.findByRole("status")).textContent).toContain(
    "If alice@example.test matches an account",
  );
  expect(posts()).toHaveLength(1);
  const [url, init] = posts()[0]!;
  expect(String(url)).toContain("/api/v2/password-resets");
  expect(JSON.parse(String(init?.body))).toEqual({ login: "alice@example.test" });
});

it("explains a server that does not offer self-service reset", async () => {
  state = "disabled";
  mount();
  await screen.findByText(/doesn't offer password resets/);
  expect(screen.queryByLabelText("Username or email")).toBeNull();
});

it("keeps the form and explains a rate limit", async () => {
  submit = async () => problem(429, "rate_limited");
  mount();
  await screen.findByLabelText("Username or email");
  send();
  expect((await screen.findByRole("alert")).textContent).toContain("Too many requests");
  expect(screen.getByLabelText("Username or email")).toBeTruthy();
});

it("switches to the unavailable notice when the server refuses the request", async () => {
  submit = async () => problem(409, "capability_disabled");
  mount();
  await screen.findByLabelText("Username or email");
  send();
  await screen.findByText(/doesn't offer password resets/);
});
