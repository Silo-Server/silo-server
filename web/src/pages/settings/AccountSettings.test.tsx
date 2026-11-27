import { act, render, screen } from "@testing-library/react";
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
  usePasswordPolicy: () => ({
    minimum_length: 8,
    maximum_bytes: 72,
    require_uppercase: false,
    require_special: false,
  }),
}));

// The Sign-in section has its own tests (AccountSignInSection.test.tsx).
vi.mock("./AccountSignInSection", () => ({ AccountSignInSection: () => null }));

vi.mock("sonner", () => ({
  toast: { success: (...args: unknown[]) => mocks.toastSuccess(...args) },
}));

import { v2Problem } from "@/api/v2/problems.test-support";
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

  it("ties a refused password to the field it is about until that field changes", async () => {
    const user = userEvent.setup();
    render(<AccountSettings />);
    const newPassword = screen.getByLabelText("New password");
    const confirmation = screen.getByLabelText("Confirm new password");

    await user.type(screen.getByLabelText("Current password"), "old password");
    await user.type(newPassword, "short");
    await user.type(confirmation, "short");
    await user.click(screen.getByRole("button", { name: "Change password" }));

    expect(newPassword).toHaveAttribute("aria-invalid", "true");
    expect(newPassword).toHaveAccessibleDescription(/Use at least 8 characters\.$/);
    await user.type(newPassword, "x");
    expect(newPassword).not.toHaveAttribute("aria-invalid");

    await user.click(screen.getByRole("button", { name: "Change password" }));
    expect(confirmation).toHaveAttribute("aria-invalid", "true");
    expect(confirmation).toHaveAccessibleDescription("New passwords do not match.");
    // Correcting the mismatch from the new password clears it too.
    await user.type(newPassword, "y");
    expect(confirmation).not.toHaveAttribute("aria-invalid");
    expect(mocks.mutateAsync).not.toHaveBeenCalled();
  });

  it("shows the server's refusal, and its own message when the server never answered", async () => {
    const user = userEvent.setup();
    render(<AccountSettings />);
    const submit = async () => {
      const fill = async (label: string, value: string) => {
        await user.clear(screen.getByLabelText(label));
        await user.type(screen.getByLabelText(label), value);
      };
      await fill("Current password", "old password");
      await fill("New password", "new password");
      await fill("Confirm new password", "new password");
      await user.click(screen.getByRole("button", { name: "Change password" }));
    };

    // As the server answers a refusal: the summary, then the member's detail.
    const refused = v2Problem(
      422,
      "validation_failed",
      "The request did not pass validation; see errors.",
    );
    refused.problem.errors = [
      {
        location: "body.new_password",
        code: "invalid",
        detail: "Include at least one capital letter.",
      },
    ];
    mocks.mutateAsync.mockRejectedValueOnce(refused);
    await submit();
    expect(screen.getByRole("alert")).toHaveTextContent("Include at least one capital letter.");
    // The server named the new password, so that field is marked.
    expect(screen.getByLabelText("New password")).toHaveAttribute("aria-invalid", "true");
    expect(screen.getByLabelText("New password")).toHaveAccessibleDescription(
      /Include at least one capital letter\.$/,
    );

    mocks.mutateAsync.mockRejectedValueOnce(new TypeError("Failed to fetch"));
    await submit();
    expect(screen.getByRole("alert")).toHaveTextContent("Failed to change password.");
  });

  it("leaves a password edited while the change was pending unmarked by its refusal", async () => {
    const user = userEvent.setup();
    let refuse: (reason: unknown) => void = () => {};
    mocks.mutateAsync.mockReturnValueOnce(
      new Promise((_resolve, reject) => {
        refuse = reject;
      }),
    );
    render(<AccountSettings />);
    const newPassword = screen.getByLabelText("New password");
    await user.type(screen.getByLabelText("Current password"), "old password");
    await user.type(newPassword, "new password");
    await user.type(screen.getByLabelText("Confirm new password"), "new password");
    await user.click(screen.getByRole("button", { name: "Change password" }));

    await user.type(newPassword, "!");
    const refused = v2Problem(
      422,
      "validation_failed",
      "The request did not pass validation; see errors.",
    );
    refused.problem.errors = [
      {
        location: "body.new_password",
        code: "invalid",
        detail: "Include at least one capital letter.",
      },
    ];
    await act(async () => refuse(refused));

    expect(screen.getByRole("alert")).toHaveTextContent("Include at least one capital letter.");
    expect(newPassword).not.toHaveAttribute("aria-invalid");
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
