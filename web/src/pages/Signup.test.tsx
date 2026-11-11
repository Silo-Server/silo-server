// @vitest-environment jsdom
import { cleanup, fireEvent, screen } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { V2ProblemError } from "@/api/v2/request";
import { renderRoutes } from "./authPage.test-support";
import Signup from "./Signup";

const auth = vi.hoisted(() => ({ signup: vi.fn(), selectProfile: vi.fn() }));
const request = vi.hoisted(() => vi.fn());
const toastError = vi.hoisted(() => vi.fn());
vi.mock("@/hooks/useAuth", () => ({
  useAuth: () => ({ ...auth, user: null, profile: null, loading: false }),
  getBootstrapProfile: () => null,
}));
vi.mock("@/hooks/queries/profiles", () => ({ listProfiles: async () => ({ profiles: [] }) }));
vi.mock("@/hooks/useServerBranding", () => ({ useServerBranding: () => ({ serverName: "Silo" }) }));
vi.mock("@/api/v2/request", async () => ({
  ...(await vi.importActual<typeof import("@/api/v2/request")>("@/api/v2/request")),
  v2: request,
}));
vi.mock("@/components/auth/AuthBackground", () => ({ AuthBackground: () => null }));
vi.mock("@/components/auth/AuthLanguagePicker", () => ({ AuthLanguagePicker: () => null }));
vi.mock("sonner", () => ({ toast: { error: toastError } }));

const valid = {
  Username: "alice",
  Email: "alice@example.test",
  Password: "long-enough",
  "Confirm password": "long-enough",
  "Invite code": "WELCOME",
};

async function fill(values: Partial<typeof valid>) {
  for (const [label, value] of Object.entries({ ...valid, ...values })) {
    const input = await screen.findByLabelText(label);
    fireEvent.change(input, { target: { value } });
    fireEvent.blur(input);
  }
}
const createAccount = () => screen.getByRole("button", { name: "Create account" });
const submit = () => fireEvent.submit(createAccount().closest("form")!);
const errorUnder = (label: string) => {
  const id = screen.getByLabelText(label).getAttribute("aria-describedby");
  return id ? document.getElementById(id)?.textContent : null;
};

beforeEach(() => {
  request.mockResolvedValue({ enabled: true });
  auth.signup.mockResolvedValue(undefined);
  renderRoutes("/signup", { "/signup": <Signup />, "/profiles": <p>Profile picker</p> });
});
afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

it("shows no rule on a form that has just loaded", async () => {
  await screen.findByLabelText("Username");
  expect(createAccount()).toHaveProperty("disabled", true);
  expect(screen.queryByText("Username is required")).toBeNull();
});

it.each([
  ["Password", "1234567", "Use at least 8 characters."],
  ["Password", "é".repeat(37), "Use no more than 72 bytes."],
])("refuses a %s outside the length rules before sending", async (label, value, rule) => {
  await fill({ Password: value, "Confirm password": value });
  expect(createAccount()).toHaveProperty("disabled", true);
  expect(errorUnder(label)).toBe(rule);
  submit();
  expect(auth.signup).not.toHaveBeenCalled();
  expect(toastError).not.toHaveBeenCalled();
});

it.each([
  ["Confirm password", { "Confirm password": "different" }, "Passwords do not match"],
  ["Email", { Email: "alice" }, "Enter a valid email address, like name@example.com"],
  ["Username", { Username: "" }, "Username is required"],
  ["Invite code", { "Invite code": " " }, "Invite code is required"],
])("keeps the button disabled and says why for %s", async (label, values, rule) => {
  await fill(values);
  expect(createAccount()).toHaveProperty("disabled", true);
  expect(errorUnder(label)).toBe(rule);
});

it("shows a refused invite code under its field", async () => {
  auth.signup.mockRejectedValue(
    new V2ProblemError("signup", {
      type: "https://siloserver.org/docs/api/v2/problems/validation_failed",
      title: "Validation failed",
      status: 422,
      detail: "Invalid invite code",
      instance: "urn:silo:request:1",
      errors: [{ location: "body.invite_code", code: "invalid", detail: "Invalid invite code" }],
    }),
  );
  await fill({});
  fireEvent.click(createAccount());
  await screen.findByText("Invalid invite code");
  expect(errorUnder("Invite code")).toBe("Invalid invite code");
  expect(toastError).not.toHaveBeenCalled();
});

it("creates the account when every field is valid", async () => {
  await fill({});
  expect(createAccount()).toHaveProperty("disabled", false);
  fireEvent.click(createAccount());
  await screen.findByText("Profile picker");
  expect(auth.signup).toHaveBeenCalledWith("alice", "alice@example.test", "long-enough", "WELCOME");
});
