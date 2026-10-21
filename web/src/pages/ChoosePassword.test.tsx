// @vitest-environment jsdom
import { cleanup, fireEvent, screen } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { V2ProblemError } from "@/api/v2/request";
import { renderRoutes } from "./authPage.test-support";
import ChoosePassword from "./ChoosePassword";

type Account = { username: string; password_change_required: boolean };
const auth = vi.hoisted(() => ({
  user: null as Account | null,
  pendingPasswordChange: null as Account | null,
  logout: vi.fn(),
  selectProfile: vi.fn(),
  settleTemporaryPassword: vi.fn(),
}));
const request = vi.hoisted(() => vi.fn());
vi.mock("@/hooks/useAuth", () => ({
  useAuth: () => ({ ...auth, loading: false }),
  getBootstrapProfile: () => null,
}));
vi.mock("@/hooks/queries/profiles", () => ({
  listProfiles: async () => ({ profiles: [] }),
}));
vi.mock("@/api/v2/request", async () => ({
  ...(await vi.importActual<typeof import("@/api/v2/request")>("@/api/v2/request")),
  v2: request,
}));
vi.mock("@/components/auth/AuthBackground", () => ({ AuthBackground: () => null }));

function mount(path = "/change-password") {
  return renderRoutes(path, {
    "/change-password": <ChoosePassword />,
    "/profiles": <p>Profile picker</p>,
    "/library": <p>Library</p>,
    "/": <p>Home</p>,
    "/login": <p>Ordinary login</p>,
  });
}
async function fill(next = "chosen-password") {
  fireEvent.change(await screen.findByLabelText("Temporary password"), {
    target: { value: "temporary-pass" },
  });
  fireEvent.change(screen.getByLabelText("New password"), { target: { value: next } });
  fireEvent.change(screen.getByLabelText("Confirm new password"), { target: { value: next } });
  fireEvent.submit(screen.getByRole("button", { name: "Save password" }).closest("form")!);
}

beforeEach(() => {
  auth.user = null;
  auth.pendingPasswordChange = { username: "alice", password_change_required: true };
  request.mockResolvedValue(undefined);
  auth.settleTemporaryPassword.mockResolvedValue(undefined);
});
afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

it("replaces the temporary password, lifts the restriction, and continues", async () => {
  mount("/change-password?redirect=%2Flibrary");
  await fill();
  await screen.findByText("Library");
  expect(request).toHaveBeenCalledWith("POST /api/v2/account/password", {
    body: { current_password: "temporary-pass", new_password: "chosen-password" },
  });
  expect(auth.settleTemporaryPassword).toHaveBeenCalled();
});

it("shows the server's reason and stays on the screen", async () => {
  request.mockRejectedValue(
    new V2ProblemError("changePassword", {
      type: "https://siloserver.org/docs/api/v2/problems/validation_failed",
      title: "Validation failed",
      status: 422,
      detail: "The request did not pass validation; see errors.",
      instance: "urn:silo:request:1",
      errors: [
        {
          location: "body.new_password",
          code: "invalid",
          detail: "Choose a password different from the temporary one.",
        },
      ],
    }),
  );
  mount();
  await fill();
  await screen.findByText("Choose a password different from the temporary one.");
  expect(auth.settleTemporaryPassword).not.toHaveBeenCalled();
});

it("sends a signed-out visitor to sign in", async () => {
  auth.pendingPasswordChange = null;
  mount();
  await screen.findByText("Ordinary login");
});

it("keeps a saved password when signing in afterwards fails, and retries only that", async () => {
  auth.settleTemporaryPassword.mockRejectedValueOnce(new Error("offline"));
  mount("/change-password?redirect=%2Flibrary");
  await fill();
  fireEvent.click(await screen.findByRole("button", { name: "Try again" }));
  await screen.findByText("Library");
  // The password change went out once; the retry only settled the session.
  expect(request).toHaveBeenCalledTimes(1);
  expect(auth.settleTemporaryPassword).toHaveBeenCalledTimes(2);
});
