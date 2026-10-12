import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { V2ProblemError } from "@/api/v2/request";

import { AccountStep } from "./AccountStep";

const useWizardContextMock = vi.fn();

vi.mock("../WizardContext", () => ({
  useWizardContext: (...args: unknown[]) => useWizardContextMock(...args),
}));

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

function mockStep(setupTokenRequired: boolean) {
  const setupInitialUser = vi.fn();
  useWizardContextMock.mockReturnValue({
    user: null,
    profile: null,
    profiles: [],
    profilesLoaded: false,
    profilesNeedPin: false,
    profilesError: false,
    retryProfiles: vi.fn(),
    selectProfile: vi.fn(),
    setupInitialUser,
    setupTokenRequired,
    setSummary: vi.fn(),
  });
  return { setupInitialUser };
}

function setupTokenProblem(id: "setup_token_required" | "setup_token_invalid") {
  return new V2ProblemError("setupServer", {
    type: `https://siloserver.org/docs/api/v2/problems/${id}`,
    title: "Setup token",
    status: 403,
    detail: "Refused.",
    instance: "test",
  });
}

async function fillAccount(user: ReturnType<typeof userEvent.setup>) {
  await user.type(screen.getByLabelText("Username"), "admin");
  await user.type(screen.getByLabelText("Email"), "admin@example.test");
  await user.type(screen.getByLabelText("Password"), "correct horse");
  await user.type(screen.getByLabelText("Confirm password"), "correct horse");
}

function renderStep() {
  return render(
    <MemoryRouter>
      <AccountStep />
    </MemoryRouter>,
  );
}

describe("AccountStep setup token", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("asks for no token on the local network", async () => {
    const user = userEvent.setup();
    const { setupInitialUser } = mockStep(false);
    renderStep();

    expect(screen.queryByLabelText("Setup token")).not.toBeInTheDocument();
    await fillAccount(user);
    await user.click(screen.getByRole("button", { name: "Create account" }));

    expect(setupInitialUser).toHaveBeenCalledWith(
      "admin",
      "admin@example.test",
      "correct horse",
      undefined,
    );
  });

  it("sends the token when the server requires one", async () => {
    const user = userEvent.setup();
    const { setupInitialUser } = mockStep(true);
    renderStep();

    await user.type(screen.getByLabelText("Setup token"), " QK3V7XN2M4PLR6TYW5HBJ8CDFA ");
    await fillAccount(user);
    await user.click(screen.getByRole("button", { name: "Create account" }));

    expect(setupInitialUser).toHaveBeenCalledWith(
      "admin",
      "admin@example.test",
      "correct horse",
      "QK3V7XN2M4PLR6TYW5HBJ8CDFA",
    );
  });

  it("shows the token field when the server refuses setup without one", async () => {
    const user = userEvent.setup();
    const { setupInitialUser } = mockStep(false);
    setupInitialUser.mockRejectedValueOnce(setupTokenProblem("setup_token_required"));
    renderStep();

    await fillAccount(user);
    await user.click(screen.getByRole("button", { name: "Create account" }));

    expect(await screen.findByLabelText("Setup token")).toBeInTheDocument();
    expect(screen.getByText(/isn't on its local network/)).toBeInTheDocument();
  });

  it("marks a wrong token invalid", async () => {
    const user = userEvent.setup();
    const { setupInitialUser } = mockStep(true);
    setupInitialUser.mockRejectedValueOnce(setupTokenProblem("setup_token_invalid"));
    renderStep();

    await user.type(screen.getByLabelText("Setup token"), "WRONG");
    await fillAccount(user);
    await user.click(screen.getByRole("button", { name: "Create account" }));

    expect(await screen.findByText(/That token doesn't match/)).toBeInTheDocument();
    expect(screen.getByLabelText("Setup token")).toHaveAttribute("aria-invalid", "true");
  });
});
