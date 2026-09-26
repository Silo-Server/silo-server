import { beforeEach, afterEach, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { setAccessToken, setProfileId, setProfileToken } from "@/api/client";
import InvitationsTab from "./InvitationsTab";
const mocks = vi.hoisted(() => ({
  create: vi.fn(),
  resend: vi.fn(),
  revoke: vi.fn(),
  reset: vi.fn(),
  restart: vi.fn(),
  next: vi.fn(),
  profile: true,
  listError: false,
  rows: true,
}));
const row = {
  id: "7",
  email: "invitee@example.invalid",
  role: "user",
  status: "pending",
  created_at: "2026-01-01T00:00:00Z",
  expires_at: "2099-01-01T00:00:00Z",
};
vi.mock("@/hooks/useAuth", () => ({ useAuth: () => ({}) }));
vi.mock("@/hooks/queries/admin/invitations", () => ({
  useInvitationCapabilities: () => ({
    data: { state: "available", default_profile: mocks.profile },
  }),
  useAdminInvitations: () => ({
    data: { pages: [{ items: mocks.rows ? [row] : [] }] },
    isError: mocks.listError,
    error: new Error("Cursor expired"),
    restart: mocks.restart,
    hasNextPage: true,
    fetchNextPage: mocks.next,
  }),
  useCreateInvitation: () => ({ mutateAsync: mocks.create, reset: mocks.reset }),
  useResendInvitation: () => ({ mutateAsync: mocks.resend, reset: mocks.reset }),
  useRevokeInvitation: () => ({ mutateAsync: mocks.revoke, reset: mocks.reset }),
}));
vi.mock("@/hooks/queries/admin/accessGroups", () => ({ useAccessGroups: () => ({ data: [] }) }));
vi.mock("@/hooks/queries/admin/libraries", () => ({ useAdminLibraries: () => ({ data: [] }) }));
beforeEach(() => {
  vi.stubGlobal(
    "ResizeObserver",
    class {
      observe() {}
      unobserve() {}
      disconnect() {}
    },
  );
  vi.clearAllMocks();
  mocks.profile = true;
  mocks.listError = false;
  mocks.rows = true;
  mocks.restart.mockResolvedValue(undefined);
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken("account");
  setProfileId("owner");
  setProfileToken(null);
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});
it("renders pending and partial history failures with explicit restart", () => {
  mocks.listError = true;
  render(<InvitationsTab />);
  expect(screen.getByText("Pending")).toBeInTheDocument();
  expect(screen.getByText(row.email)).toBeInTheDocument();
  expect(screen.queryByText(/No invitations yet/)).not.toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "Reload history" }));
  expect(mocks.restart).toHaveBeenCalledTimes(1);
});
it("retains revoke confirmation and row on failure", async () => {
  mocks.revoke.mockRejectedValue(new Error("Could not revoke"));
  render(<InvitationsTab />);
  fireEvent.click(screen.getByTitle("Revoke this link"));
  fireEvent.click(screen.getByRole("button", { name: "Revoke" }));
  expect(await screen.findByText("Could not revoke")).toBeInTheDocument();
  expect(screen.getByRole("dialog")).toBeInTheDocument();
  expect(screen.getByText(row.email)).toBeInTheDocument();
  expect(mocks.revoke).toHaveBeenCalledWith(
    expect.objectContaining({
      id: "7",
      profileContext: expect.objectContaining({ profileId: "owner" }),
    }),
  );
});
it("keeps a one-time link after clipboard failure, resets mutation state, and disposes on scope change", async () => {
  mocks.resend.mockResolvedValue({
    invitation: row,
    claim_url: "https://example.invalid/invite/one-time",
    delivery_status: "failed_or_unknown",
  });
  Object.defineProperty(navigator, "clipboard", {
    configurable: true,
    value: { writeText: vi.fn().mockRejectedValue(new Error("denied")) },
  });
  const view = render(<InvitationsTab />);
  fireEvent.click(screen.getByTitle("Resend with a fresh link"));
  expect(await screen.findByText(/email delivery failed or is uncertain/)).toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "Copy link" }));
  expect(await screen.findByText(/Could not copy/)).toBeInTheDocument();
  expect(screen.getByText("https://example.invalid/invite/one-time")).toBeInTheDocument();
  expect(mocks.reset).toHaveBeenCalled();
  setProfileId("other");
  view.rerender(<InvitationsTab />);
  expect(screen.queryByText("https://example.invalid/invite/one-time")).not.toBeInTheDocument();
});
it("blocks synchronous double submit and dismissal until delivery resolves", async () => {
  let resolve!: (value: unknown) => void;
  mocks.create.mockImplementation(
    () =>
      new Promise((r) => {
        resolve = r;
      }),
  );
  render(<InvitationsTab />);
  fireEvent.click(screen.getByRole("button", { name: "Invite someone" }));
  fireEvent.change(screen.getByLabelText("Email address"), { target: { value: row.email } });
  const submit = screen.getByRole("button", { name: "Send invite" });
  fireEvent.click(submit);
  fireEvent.click(submit);
  expect(mocks.create).toHaveBeenCalledTimes(1);
  fireEvent.keyDown(screen.getByRole("dialog"), { key: "Escape" });
  expect(screen.getByRole("dialog")).toBeInTheDocument();
  await act(async () =>
    resolve({
      invitation: row,
      claim_url: "https://example.invalid/invite/created",
      delivery_status: "not_configured",
    }),
  );
  expect(await screen.findByText(/deliver this link yourself/)).toBeInTheDocument();
});
it("guides unsupported profile creation and preserves false booleans", async () => {
  mocks.profile = false;
  mocks.create.mockResolvedValue({
    invitation: row,
    claim_url: "https://example.invalid/invite/profileless",
    delivery_status: "sent",
  });
  render(<InvitationsTab />);
  fireEvent.click(screen.getByRole("button", { name: "Invite someone" }));
  expect(screen.getByRole("status")).toHaveTextContent("cannot create a default profile");
  fireEvent.change(screen.getByLabelText("Email address"), { target: { value: row.email } });
  fireEvent.click(screen.getByLabelText("Show the feature tour on first sign-in"));
  fireEvent.click(screen.getByRole("button", { name: "Send invite" }));
  await waitFor(() =>
    expect(mocks.create).toHaveBeenCalledWith(
      expect.objectContaining({
        body: expect.objectContaining({ create_profile: false, show_tour: false }),
      }),
    ),
  );
  expect(mocks.create.mock.calls[0]![0].body).not.toHaveProperty("library_ids", null);
});
