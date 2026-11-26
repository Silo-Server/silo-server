// @vitest-environment jsdom
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, it, vi } from "vitest";

import { setAccessToken, setProfileId } from "@/api/client";
import { v2Problem } from "@/api/v2/problems.test-support";

import { Dialog } from "@/components/ui/dialog";

import { CreateUserDialogContent } from "./CreateUserForm";

const create = vi.hoisted(() => vi.fn());

vi.mock("@/hooks/queries/admin/users", () => ({
  useAdminPolicyDefaults: () => ({ data: undefined }),
  useAdminUserCapabilities: () => ({ data: { available: true, default_profile: true } }),
  useCreateUser: () => ({ mutateAsync: create, isPending: false }),
  useViewerIsOwner: () => true,
}));
vi.mock("@/hooks/queries/admin/libraries", () => ({ useAdminLibraries: () => ({ data: [] }) }));
vi.mock("@/hooks/queries/admin/accessGroups", () => ({
  useAccessGroups: () => ({ data: [], isSuccess: true }),
}));
vi.mock("@/hooks/useAuth", () => ({ useAuth: () => ({ user: { id: 1 } }) }));
vi.mock("@/hooks/queries/account", () => ({
  usePasswordPolicy: () => ({
    minimum_length: 12,
    maximum_bytes: 72,
    require_uppercase: true,
    require_special: false,
  }),
}));

class MockResizeObserver implements ResizeObserver {
  observe() {}
  unobserve() {}
  disconnect() {}
}

beforeEach(() => {
  vi.stubGlobal("ResizeObserver", MockResizeObserver);
  setAccessToken("account");
  setProfileId("owner");
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

it("shows a password the policy refuses on the Account tab, at the field", async () => {
  const user = userEvent.setup();
  render(
    <Dialog open>
      <CreateUserDialogContent onClose={() => {}} onBusy={() => {}} />
    </Dialog>,
  );
  await user.type(screen.getByLabelText("Username"), "bob");
  await user.type(screen.getByLabelText("Email"), "bob@example.test");
  await user.type(screen.getByLabelText("Password"), "lowercase-only-pw");
  // Saved from another tab, the password field is out of sight.
  await user.click(screen.getByRole("tab", { name: "Limits" }));
  await user.click(screen.getByRole("button", { name: "Save" }));

  expect(screen.getByRole("tab", { name: "Account" })).toHaveAttribute("aria-selected", "true");
  const password = screen.getByLabelText("Password");
  expect(password).toHaveAttribute("aria-invalid", "true");
  expect(password).toHaveAccessibleErrorMessage("Include at least one capital letter.");
  // The live rules, then the refusal.
  expect(password).toHaveAccessibleDescription(
    /^At least 12 characters\..*Include at least one capital letter\.$/,
  );
  expect(create).not.toHaveBeenCalled();

  await user.type(password, "X");
  expect(password).not.toHaveAttribute("aria-invalid");
  expect(screen.queryByText("Include at least one capital letter.")).not.toBeInTheDocument();
});

it("shows a password the server refuses at the field", async () => {
  // The policy tightened after the form loaded its copy.
  const refused = v2Problem(
    422,
    "validation_failed",
    "The request did not pass validation; see errors.",
  );
  refused.problem.errors = [
    {
      location: "body.password",
      code: "invalid",
      detail: "Include at least one special character, such as ! or #.",
    },
  ];
  create.mockRejectedValueOnce(refused);
  const user = userEvent.setup();
  render(
    <Dialog open>
      <CreateUserDialogContent onClose={() => {}} onBusy={() => {}} />
    </Dialog>,
  );
  await user.type(screen.getByLabelText("Username"), "bob");
  await user.type(screen.getByLabelText("Email"), "bob@example.test");
  await user.type(screen.getByLabelText("Password"), "UppercaseOnlyPw1");
  await user.click(screen.getByRole("button", { name: "Save" }));

  const password = await screen.findByLabelText("Password");
  expect(create).toHaveBeenCalled();
  expect(password).toHaveAttribute("aria-invalid", "true");
  expect(password).toHaveAccessibleErrorMessage(
    "Include at least one special character, such as ! or #.",
  );
});

it("shows a refusal that arrives after the admin left the Account tab", async () => {
  const refused = v2Problem(
    422,
    "validation_failed",
    "The request did not pass validation; see errors.",
  );
  refused.problem.errors = [
    { location: "body.password", code: "invalid", detail: "Include at least one capital letter." },
  ];
  let reject: (err: unknown) => void = () => {};
  create.mockReturnValueOnce(new Promise((_, r) => (reject = r)));
  const user = userEvent.setup();
  render(
    <Dialog open>
      <CreateUserDialogContent onClose={() => {}} onBusy={() => {}} />
    </Dialog>,
  );
  await user.type(screen.getByLabelText("Username"), "bob");
  await user.type(screen.getByLabelText("Email"), "bob@example.test");
  await user.type(screen.getByLabelText("Password"), "UppercaseOnlyPw1");
  await user.click(screen.getByRole("button", { name: "Save" }));
  await user.click(screen.getByRole("tab", { name: "Limits" }));
  expect(screen.queryByLabelText("Password")).toBeNull();
  reject(refused);

  const password = await screen.findByLabelText("Password");
  expect(password).toHaveAccessibleErrorMessage("Include at least one capital letter.");
});
