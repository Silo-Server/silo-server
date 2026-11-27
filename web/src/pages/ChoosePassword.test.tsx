import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, expect, it, vi } from "vitest";
import { v2Problem } from "@/api/v2/problems.test-support";
import ChoosePassword from "./ChoosePassword";

const mutateAsync = vi.fn();
vi.mock("@/hooks/useAuth", () => ({
  useAuth: () => ({
    user: null,
    pendingPasswordChange: { username: "sam" },
    loading: false,
    logout: vi.fn(),
    settleTemporaryPassword: vi.fn(),
  }),
}));
vi.mock("@/hooks/usePostSignInNavigation", () => ({ usePostSignInNavigation: () => vi.fn() }));
vi.mock("@/hooks/queries/account", () => ({
  useChangeAccountPassword: () => ({ mutateAsync, isPending: false }),
  usePasswordPolicy: () => ({
    minimum_length: 8,
    maximum_bytes: 72,
    require_uppercase: false,
    require_special: false,
  }),
}));
vi.mock("@/components/auth/AuthBackground", () => ({ AuthBackground: () => null }));

afterEach(() => {
  cleanup();
  mutateAsync.mockReset();
});

function send() {
  fireEvent.submit(screen.getByRole("button", { name: "Save password" }).closest("form")!);
}

it("ties a refused password to the field it is about until that field changes", () => {
  render(
    <MemoryRouter>
      <ChoosePassword />
    </MemoryRouter>,
  );
  const password = screen.getByLabelText("New password");
  const confirmation = screen.getByLabelText("Confirm new password");
  fireEvent.change(screen.getByLabelText("Temporary password"), { target: { value: "temp" } });
  fireEvent.change(password, { target: { value: "short" } });
  fireEvent.change(confirmation, { target: { value: "short" } });
  send();

  expect(screen.getByRole("alert")).toHaveTextContent("Use at least 8 characters.");
  expect(password).toHaveAttribute("aria-invalid", "true");
  expect(password).toHaveAccessibleDescription("Use at least 8 characters.");
  expect(confirmation).not.toHaveAttribute("aria-invalid");

  fireEvent.change(password, { target: { value: "long enough" } });
  expect(password).not.toHaveAttribute("aria-invalid");

  send();
  expect(screen.getByRole("alert")).toHaveTextContent("Passwords do not match.");
  expect(confirmation).toHaveAttribute("aria-invalid", "true");
  expect(confirmation).toHaveAccessibleDescription("Passwords do not match.");
  // Correcting the mismatch from the new password clears it too.
  fireEvent.change(password, { target: { value: "short" } });
  expect(confirmation).not.toHaveAttribute("aria-invalid");
  expect(mutateAsync).not.toHaveBeenCalled();
});

it("marks the new password the server refused", async () => {
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
  mutateAsync.mockRejectedValueOnce(refused);
  render(
    <MemoryRouter>
      <ChoosePassword />
    </MemoryRouter>,
  );
  const password = screen.getByLabelText("New password");
  fireEvent.change(screen.getByLabelText("Temporary password"), { target: { value: "temp" } });
  fireEvent.change(password, { target: { value: "long enough" } });
  fireEvent.change(screen.getByLabelText("Confirm new password"), {
    target: { value: "long enough" },
  });
  send();

  expect(await screen.findByRole("alert")).toHaveTextContent(
    "Include at least one capital letter.",
  );
  expect(password).toHaveAttribute("aria-invalid", "true");
  expect(password).toHaveAccessibleDescription("Include at least one capital letter.");
});
