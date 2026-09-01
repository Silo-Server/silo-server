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

vi.mock("sonner", () => ({
  toast: { success: (...args: unknown[]) => mocks.toastSuccess(...args) },
}));

import AccountSettings from "./AccountSettings";

describe("AccountSettings", () => {
  beforeEach(() => {
    mocks.mutateAsync.mockReset().mockResolvedValue(undefined);
    mocks.toastSuccess.mockReset();
    mocks.useCapability.mockReset().mockReturnValue({
      data: {
        schema_version: 1,
        change_password: true,
        requires_current_password: true,
        minimum_password_length: 8,
        maximum_password_bytes: 72,
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
        schema_version: 1,
        change_password: false,
        requires_current_password: true,
        minimum_password_length: 8,
        maximum_password_bytes: 72,
      },
      isLoading: false,
      isError: false,
    });

    render(<AccountSettings />);

    expect(screen.getByText("Local password changes are unavailable.")).toBeInTheDocument();
    expect(screen.queryByLabelText("Current password")).not.toBeInTheDocument();
  });
});
