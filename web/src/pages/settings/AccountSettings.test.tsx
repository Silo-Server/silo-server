import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  useCapability: vi.fn(),
  useChangePassword: vi.fn(),
  mutateAsync: vi.fn(),
  toastSuccess: vi.fn(),
}));

vi.mock("@/hooks/queries/account", () => ({
  useAccountPasswordCapability: () => mocks.useCapability(),
  useChangeAccountPassword: () => mocks.useChangePassword(),
}));

// The Sign-in section has its own tests (AccountSignInSection.test.tsx).
vi.mock("./AccountSignInSection", () => ({ AccountSignInSection: () => null }));

vi.mock("sonner", () => ({
  toast: { success: (...args: unknown[]) => mocks.toastSuccess(...args) },
}));

import AccountSettings from "./AccountSettings";

class MockResizeObserver implements ResizeObserver {
  observe() {}
  unobserve() {}
  disconnect() {}
}

describe("AccountSettings", () => {
  beforeEach(() => {
    // The Radix checkbox measures itself.
    vi.stubGlobal("ResizeObserver", MockResizeObserver);
    mocks.mutateAsync.mockReset().mockResolvedValue(undefined);
    mocks.toastSuccess.mockReset();
    mocks.useCapability.mockReset().mockReturnValue({
      data: {
        revision: "1",
        state: "available",
        allowed: true,
        requires_current_password: true,
        minimum_password_length: 8,
        maximum_password_bytes: 72,
        sign_out_other_sessions: true,
      },
      isLoading: false,
      isError: false,
    });
    mocks.useChangePassword.mockReset().mockReturnValue({
      mutateAsync: mocks.mutateAsync,
      isPending: false,
    });
  });

  it("changes the shared account password", async () => {
    const user = userEvent.setup();
    render(<AccountSettings />);

    await user.type(screen.getByLabelText("Current password"), "old password");
    await user.type(screen.getByLabelText("New password"), "new password");
    await user.type(screen.getByLabelText("Confirm new password"), "new password");
    await user.click(screen.getByRole("button", { name: "Change password" }));

    expect(mocks.mutateAsync).toHaveBeenCalledWith({
      current_password: "old password",
      new_password: "new password",
    });
    expect(mocks.toastSuccess).toHaveBeenCalledWith("Password changed");
    expect(screen.getByLabelText("Current password")).toHaveValue("");
  });

  it("signs out of other devices only when asked", async () => {
    const user = userEvent.setup();
    render(<AccountSettings />);

    const signOut = screen.getByRole("checkbox", { name: "Sign out of other devices" });
    expect(signOut).not.toBeChecked();
    await user.type(screen.getByLabelText("Current password"), "old password");
    await user.type(screen.getByLabelText("New password"), "new password");
    await user.type(screen.getByLabelText("Confirm new password"), "new password");
    await user.click(signOut);
    await user.click(screen.getByRole("button", { name: "Change password" }));

    expect(mocks.mutateAsync).toHaveBeenCalledWith({
      current_password: "old password",
      new_password: "new password",
      sign_out_other_sessions: true,
    });
  });

  it("stops mismatched passwords before calling the API", async () => {
    const user = userEvent.setup();
    render(<AccountSettings />);

    await user.type(screen.getByLabelText("Current password"), "old password");
    await user.type(screen.getByLabelText("New password"), "new password");
    await user.type(screen.getByLabelText("Confirm new password"), "different password");
    await user.click(screen.getByRole("button", { name: "Change password" }));

    expect(screen.getByRole("alert")).toHaveTextContent("New passwords do not match.");
    expect(mocks.mutateAsync).not.toHaveBeenCalled();
  });

  it("does not render the form when the server denies the capability", () => {
    mocks.useCapability.mockReturnValue({
      data: {
        revision: "1",
        state: "available",
        allowed: false,
        requires_current_password: true,
        minimum_password_length: 8,
        maximum_password_bytes: 72,
        sign_out_other_sessions: true,
      },
      isLoading: false,
      isError: false,
    });

    render(<AccountSettings />);

    expect(screen.getByText("Local password changes are unavailable.")).toBeInTheDocument();
    expect(screen.queryByLabelText("Current password")).not.toBeInTheDocument();
  });
});
