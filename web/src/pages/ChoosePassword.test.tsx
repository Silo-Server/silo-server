// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter, Route, Routes } from "react-router";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { V2ProblemError } from "@/api/v2/request";
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
  return render(
    <QueryClientProvider client={new QueryClient()}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          <Route path="/change-password" element={<ChoosePassword />} />
          <Route path="/profiles" element={<p>Profile picker</p>} />
          <Route path="/library" element={<p>Library</p>} />
          <Route path="/" element={<p>Home</p>} />
          <Route path="/login" element={<p>Ordinary login</p>} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
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

it("leaves an account without a temporary password alone", async () => {
  auth.pendingPasswordChange = null;
  auth.user = { username: "alice", password_change_required: false };
  mount();
  await screen.findByText("Home");
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
