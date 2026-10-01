// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import type { ProfileRequestContextSnapshot } from "@/api/client";
import type { AdminUser } from "@/api/types";
import { V2ProblemError } from "@/api/v2/request";
import { AdminUserPasswordResetDialog } from "./AdminUserPasswordResetDialog";
import linkFixture from "../../../contracts/api/v2/fixtures/admin_password_reset_link.json";
import emailFixture from "../../../contracts/api/v2/fixtures/admin_password_reset_email.json";

const issue = vi.hoisted(() => vi.fn());
const copy = vi.hoisted(() => vi.fn());
const read = vi.hoisted(() => vi.fn());
const update = vi.hoisted(() => vi.fn());
vi.mock("@/api/v2/adminUsers", async () => ({
  ...(await vi.importActual<typeof import("@/api/v2/adminUsers")>("@/api/v2/adminUsers")),
  issueAdminPasswordReset: issue,
  getAdminUser: read,
  updateAdminUser: update,
}));
vi.mock("@/lib/clipboard", () => ({ copyTextToClipboard: copy }));

const user = {
  id: 7,
  username: "sample",
  email: "sample@example.test",
  enabled: true,
  password_login: true,
} as AdminUser;
const context = { profileId: "owner" } as unknown as ProfileRequestContextSnapshot;

function mount(props: Partial<Parameters<typeof AdminUserPasswordResetDialog>[0]> = {}) {
  return render(
    <MemoryRouter>
      <QueryClientProvider client={new QueryClient()}>
        <AdminUserPasswordResetDialog
          user={user}
          emailAvailable
          linkAvailable
          profileContext={context}
          onClose={vi.fn()}
          {...props}
        />
      </QueryClientProvider>
    </MemoryRouter>,
  );
}

beforeEach(() => {
  copy.mockResolvedValue(undefined);
  read.mockResolvedValue({ user, etag: '"fresh"', profileContext: context });
  update.mockResolvedValue(undefined);
});
afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

it("offers three ways back in and starts on email", () => {
  mount();
  expect(screen.getByRole("dialog", { name: "Reset password for sample" })).toBeInTheDocument();
  expect(screen.getByRole("radio", { name: "Email a reset link" })).toBeChecked();
  expect(screen.getByRole("radio", { name: "Create a link to share" })).not.toBeChecked();
  expect(screen.getByRole("radio", { name: "Set a temporary password" })).not.toBeChecked();
  expect(screen.getByText(/Sent to sample@example.test. Works once/)).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Email link" })).toBeEnabled();
});

it("creates a link to share and copies it", async () => {
  issue.mockResolvedValue(linkFixture);
  mount();
  fireEvent.click(screen.getByRole("radio", { name: "Create a link to share" }));
  fireEvent.click(screen.getByRole("button", { name: "Create link" }));
  await screen.findByText(linkFixture.reset_url);
  expect(issue).toHaveBeenCalledWith(7, "link");
  fireEvent.click(screen.getByRole("button", { name: /Copy link/ }));
  expect(copy).toHaveBeenCalledWith(linkFixture.reset_url);
});

it("emails the link without showing it", async () => {
  issue.mockResolvedValue(emailFixture);
  mount();
  fireEvent.click(screen.getByRole("button", { name: "Email link" }));
  await screen.findByText(/Sent a reset link to sample@example.test/);
  expect(screen.queryByRole("button", { name: /Copy link/ })).toBeNull();
  expect(screen.getByRole("button", { name: "Done" })).toBeInTheDocument();
});

it("keeps every option after an unconfirmed email", async () => {
  issue.mockResolvedValue({ ...emailFixture, delivery_status: "failed_or_unknown" });
  mount();
  fireEvent.click(screen.getByRole("button", { name: "Email link" }));
  await screen.findByText(/did not confirm delivery/);
  expect(screen.getByRole("radio", { name: "Create a link to share" })).toBeEnabled();
  expect(screen.getByRole("button", { name: "Email link" })).toBeEnabled();
});

it("explains why a delivery is unavailable", () => {
  mount({ emailAvailable: false });
  expect(screen.getByRole("radio", { name: "Email a reset link" })).toBeDisabled();
  expect(screen.getByText(/Set up email in Settings/)).toBeInTheDocument();
  // The first available option is picked instead.
  expect(screen.getByRole("radio", { name: "Create a link to share" })).toBeChecked();
});

it("offers only a temporary password for a disabled account", () => {
  // The server refuses reset links for a disabled account.
  mount({ user: { ...user, enabled: false } });
  expect(screen.getByRole("radio", { name: "Email a reset link" })).toBeDisabled();
  expect(screen.getByRole("radio", { name: "Create a link to share" })).toBeDisabled();
  expect(screen.getByRole("radio", { name: "Set a temporary password" })).toBeChecked();
  expect(screen.getByText("Enable the account to send it a reset link.")).toBeInTheDocument();
});

it("points to General settings while no public URL is set", () => {
  // Without a link base the server reports neither delivery as available.
  mount({ emailAvailable: false, linkAvailable: false });
  expect(screen.getByRole("radio", { name: "Create a link to share" })).toBeDisabled();
  expect(screen.getByRole("radio", { name: "Email a reset link" })).toBeDisabled();
  expect(screen.getByRole("radio", { name: "Set a temporary password" })).toBeChecked();
  expect(screen.getByText(/Set the Silo public URL to create reset links/)).toBeInTheDocument();
  expect(screen.getByRole("link", { name: /General settings/ })).toHaveAttribute(
    "href",
    "/admin/settings/general",
  );
});

it("measures a temporary password the way the server does", async () => {
  const ui = userEvent.setup();
  mount();
  await ui.click(screen.getByRole("radio", { name: "Set a temporary password" }));
  const setPassword = screen.getByRole("button", { name: "Set password" });
  const input = screen.getByLabelText("Temporary password");
  // Four emoji are eight UTF-16 units but only four characters.
  fireEvent.change(input, { target: { value: "😀😀😀😀" } });
  expect(setPassword).toBeDisabled();
  // Twenty-five CJK characters are 75 UTF-8 bytes, over bcrypt's 72.
  fireEvent.change(input, { target: { value: "密".repeat(25) } });
  expect(setPassword).toBeDisabled();
  fireEvent.change(input, { target: { value: "密".repeat(24) } });
  expect(setPassword).toBeEnabled();
});

it("sets a temporary password the account must replace, with a fresh validator", async () => {
  const ui = userEvent.setup();
  mount();
  await ui.click(screen.getByRole("radio", { name: "Set a temporary password" }));
  const setPassword = screen.getByRole("button", { name: "Set password" });
  expect(setPassword).toBeDisabled();
  const input = screen.getByLabelText("Temporary password");
  await ui.type(input, "short");
  expect(setPassword).toBeDisabled();
  await ui.type(input, "-enough");
  expect(screen.getByRole("checkbox", { name: /must choose a new one/ })).toBeChecked();
  await ui.click(setPassword);

  expect(await screen.findByRole("status")).toHaveTextContent(
    "Password set. sample is signed out everywhere. They must choose a new one at next sign-in.",
  );
  expect(read).toHaveBeenCalledWith(7, context);
  expect(update).toHaveBeenCalledWith(
    { user, etag: '"fresh"', profileContext: context },
    { password: "short-enough", require_password_change: true },
  );
  expect(screen.getByRole("button", { name: "Done" })).toBeInTheDocument();
});

it("sets a password without requiring a change when the box is cleared", async () => {
  const ui = userEvent.setup();
  mount();
  await ui.click(screen.getByRole("radio", { name: "Set a temporary password" }));
  await ui.type(screen.getByLabelText("Temporary password"), "long-password");
  await ui.click(screen.getByRole("checkbox", { name: /must choose a new one/ }));
  await ui.click(screen.getByRole("button", { name: "Set password" }));
  expect(await screen.findByRole("status")).toHaveTextContent(
    /^Password set. sample is signed out everywhere.$/,
  );
  expect(update.mock.calls[0]![1]).toEqual({
    password: "long-password",
    require_password_change: undefined,
  });
});

it("asks to try again when the account changed", async () => {
  update.mockRejectedValue(
    new V2ProblemError("updateAdminUser", {
      type: "https://silo.example/problems/precondition_failed",
      title: "Changed",
      status: 412,
      detail: "Reload",
      instance: "/api/v2/admin/users/7",
    }),
  );
  const ui = userEvent.setup();
  mount();
  await ui.click(screen.getByRole("radio", { name: "Set a temporary password" }));
  await ui.type(screen.getByLabelText("Temporary password"), "long-password");
  await ui.click(screen.getByRole("button", { name: "Set password" }));
  expect(await screen.findByRole("alert")).toHaveTextContent("The account changed. Try again.");
  await waitFor(() => expect(screen.getByRole("button", { name: "Set password" })).toBeEnabled());
});
