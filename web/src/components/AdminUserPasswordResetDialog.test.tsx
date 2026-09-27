// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import type { AdminUser } from "@/api/types";
import { AdminUserPasswordResetDialog } from "./AdminUserPasswordResetDialog";
import linkFixture from "../../../contracts/api/v2/fixtures/admin_password_reset_link.json";
import emailFixture from "../../../contracts/api/v2/fixtures/admin_password_reset_email.json";

const issue = vi.hoisted(() => vi.fn());
const copy = vi.hoisted(() => vi.fn());
vi.mock("@/api/v2/adminUsers", async () => ({
  ...(await vi.importActual<typeof import("@/api/v2/adminUsers")>("@/api/v2/adminUsers")),
  issueAdminPasswordReset: issue,
}));
vi.mock("@/lib/clipboard", () => ({ copyTextToClipboard: copy }));

const user = {
  id: 7,
  username: "sample",
  email: "sample@example.test",
  password_login: true,
} as AdminUser;

function mount(props: Partial<Parameters<typeof AdminUserPasswordResetDialog>[0]> = {}) {
  return render(
    <QueryClientProvider client={new QueryClient()}>
      <AdminUserPasswordResetDialog
        user={user}
        emailAvailable
        linkAvailable
        onClose={vi.fn()}
        {...props}
      />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  copy.mockResolvedValue(undefined);
});
afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

it("creates a link to share and copies it", async () => {
  issue.mockResolvedValue(linkFixture);
  mount();
  fireEvent.click(screen.getByRole("button", { name: "Create link to share" }));
  await screen.findByText(linkFixture.reset_url);
  expect(issue).toHaveBeenCalledWith(7, "link");
  fireEvent.click(screen.getByRole("button", { name: /Copy link/ }));
  expect(copy).toHaveBeenCalledWith(linkFixture.reset_url);
});

it("emails the link without showing it", async () => {
  issue.mockResolvedValue(emailFixture);
  mount();
  fireEvent.click(screen.getByRole("button", { name: "Email reset link" }));
  await screen.findByText(/Sent a reset link to sample@example.test/);
  expect(screen.queryByRole("button", { name: /Copy link/ })).toBeNull();
  expect(screen.getByRole("button", { name: "Done" })).toBeInTheDocument();
});

it("keeps both actions after an unconfirmed email", async () => {
  issue.mockResolvedValue({ ...emailFixture, delivery_status: "failed_or_unknown" });
  mount();
  fireEvent.click(screen.getByRole("button", { name: "Email reset link" }));
  await screen.findByText(/did not confirm delivery/);
  expect(screen.getByRole("button", { name: "Create link to share" })).not.toBeDisabled();
  expect(screen.getByRole("button", { name: "Email reset link" })).not.toBeDisabled();
});

it("explains why a delivery is unavailable", () => {
  mount({ emailAvailable: false });
  expect(screen.getByRole("button", { name: "Email reset link" })).toBeDisabled();
  expect(screen.getByText(/Set up email in Settings/)).toBeInTheDocument();
});
