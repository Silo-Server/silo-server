import { beforeEach, afterEach, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
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
  emailDelivery: true,
  listError: false,
  rows: true,
  linkRow: false,
  publicURL: "https://media.example.test" as string | undefined,
}));
const linkRow = {
  id: "8",
  email: "",
  delivery: "link",
  note: "For Sam",
  role: "user",
  status: "pending",
  created_at: "2026-01-02T00:00:00Z",
  expires_at: "2099-01-01T00:00:00Z",
};
const row = {
  id: "7",
  email: "invitee@example.invalid",
  delivery: "email_sent",
  note: "",
  role: "user",
  status: "pending",
  created_at: "2026-01-01T00:00:00Z",
  expires_at: "2099-01-01T00:00:00Z",
};
vi.mock("@/hooks/useAuth", () => ({ useAuth: () => ({}) }));
vi.mock("@/hooks/queries/admin/invitations", () => ({
  useInvitationCapabilities: () => ({
    data: {
      state: "available",
      default_profile: mocks.profile,
      email_delivery: mocks.emailDelivery,
    },
  }),
  useAdminInvitations: () => ({
    data: { pages: [{ items: mocks.rows ? (mocks.linkRow ? [linkRow, row] : [row]) : [] }] },
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
vi.mock("@/hooks/queries/admin/settings", () => ({
  useAdminServerSettings: () => ({
    data: mocks.publicURL === undefined ? undefined : { "server.public_url": mocks.publicURL },
  }),
}));
vi.mock("@/hooks/queries/admin/accessGroups", () => ({ useAccessGroups: () => ({ data: [] }) }));
vi.mock("@/hooks/queries/admin/libraries", () => ({ useAdminLibraries: () => ({ data: [] }) }));
// The viewer is the server Owner, who may invite an admin.
vi.mock("@/hooks/queries/admin/users", () => ({ useViewerIsOwner: () => true }));
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
  mocks.emailDelivery = true;
  mocks.listError = false;
  mocks.rows = true;
  mocks.linkRow = false;
  mocks.publicURL = "https://media.example.test";
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
  render(<InvitationsTab />, { wrapper: MemoryRouter });
  expect(screen.getByText("Pending")).toBeInTheDocument();
  expect(screen.getByText(row.email)).toBeInTheDocument();
  expect(screen.queryByText(/No invitations yet/)).not.toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "Reload history" }));
  expect(mocks.restart).toHaveBeenCalledTimes(1);
});
it("retains revoke confirmation and row on failure", async () => {
  mocks.revoke.mockRejectedValue(new Error("Could not revoke"));
  render(<InvitationsTab />, { wrapper: MemoryRouter });
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
  const view = render(<InvitationsTab />, { wrapper: MemoryRouter });
  fireEvent.click(screen.getByTitle("Email a new link"));
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
  render(<InvitationsTab />, { wrapper: MemoryRouter });
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
  render(<InvitationsTab />, { wrapper: MemoryRouter });
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
it("asks for the public URL before anyone fills in an invitation", () => {
  mocks.publicURL = "";
  render(<InvitationsTab />, { wrapper: MemoryRouter });
  expect(screen.getByRole("button", { name: "Invite someone" })).toBeDisabled();
  expect(screen.getByTitle("Copy a new link without emailing")).toBeDisabled();
  expect(screen.getByTitle("Email a new link")).toBeDisabled();
  expect(screen.getByTitle("Revoke this link")).not.toBeDisabled();
  expect(
    screen.getByText(/Set the Silo public URL to create invitation links/),
  ).toBeInTheDocument();
  expect(screen.getByRole("link", { name: /General settings/ })).toHaveAttribute(
    "href",
    "/admin/settings/general",
  );
});
it("does not block invitations while the settings are unknown", () => {
  mocks.publicURL = undefined;
  render(<InvitationsTab />, { wrapper: MemoryRouter });
  expect(screen.getByRole("button", { name: "Invite someone" })).not.toBeDisabled();
  expect(screen.queryByText(/Set the Silo public URL/)).toBeNull();
});
it("creates a link invitation without an address and shows the link to share", async () => {
  mocks.create.mockResolvedValue({
    invitation: linkRow,
    claim_url: "https://example.invalid/invite/link-only",
    delivery_status: "not_requested",
  });
  render(<InvitationsTab />, { wrapper: MemoryRouter });
  fireEvent.click(screen.getByRole("button", { name: "Invite someone" }));
  expect(screen.getByLabelText("Send email")).toBeChecked();
  fireEvent.click(screen.getByLabelText("Create link"));
  expect(screen.queryByLabelText("Email address")).toBeNull();
  fireEvent.change(screen.getByLabelText("Note (optional)"), { target: { value: "For Sam" } });
  fireEvent.click(screen.getByRole("button", { name: "Create link" }));
  await waitFor(() => expect(mocks.create).toHaveBeenCalledTimes(1));
  const body = mocks.create.mock.calls[0]![0].body;
  expect(body).toMatchObject({ delivery: "link", note: "For Sam" });
  expect(body.email).toBeUndefined();
  expect(await screen.findByText(/Invitation link created/)).toBeInTheDocument();
  expect(screen.getByText("https://example.invalid/invite/link-only")).toBeInTheDocument();
  expect(screen.getByText(/Anyone with the link can use it/)).toBeInTheDocument();
});
it("offers only link creation when email is not configured", () => {
  mocks.emailDelivery = false;
  render(<InvitationsTab />, { wrapper: MemoryRouter });
  fireEvent.click(screen.getByRole("button", { name: "Invite someone" }));
  expect(screen.getByLabelText("Create link")).toBeChecked();
  expect(screen.getByLabelText("Send email")).toBeDisabled();
  expect(screen.getByText("Email isn't set up on this server.")).toBeInTheDocument();
  expect(screen.getByRole("link", { name: /Notifications settings/ })).toHaveAttribute(
    "href",
    "/admin/settings/notifications",
  );
  expect(screen.getByRole("button", { name: "Create link" })).toBeInTheDocument();
});
it("shows delivery and identifies link invitations in the history", () => {
  mocks.linkRow = true;
  render(<InvitationsTab />, { wrapper: MemoryRouter });
  expect(screen.getByText("Link invitation")).toBeInTheDocument();
  expect(screen.getByText(/“For Sam”/)).toBeInTheDocument();
  expect(screen.getByText("Link created")).toBeInTheDocument();
  expect(screen.getByText("Email sent")).toBeInTheDocument();
  expect(screen.getByTitle("Replace with a new link")).toBeInTheDocument();
  fireEvent.click(screen.getAllByTitle("Revoke this link")[0]!);
  expect(
    screen.getByText("Revoke this link invitation? The link will stop working."),
  ).toBeInTheDocument();
});
it("keeps a link invitation's note after it is accepted", () => {
  mocks.linkRow = true;
  linkRow.email = "sam@example.invalid";
  linkRow.status = "accepted";
  try {
    render(<InvitationsTab />, { wrapper: MemoryRouter });
    expect(screen.getByText("sam@example.invalid")).toBeInTheDocument();
    expect(screen.getByText(/Joined by link · “For Sam”/)).toBeInTheDocument();
  } finally {
    linkRow.email = "";
    linkRow.status = "pending";
  }
});
it("replaces an emailed invitation's link without emailing it", async () => {
  mocks.resend.mockResolvedValue({
    invitation: { ...row, delivery: "link" },
    claim_url: "https://example.invalid/invite/replaced",
    delivery_status: "not_requested",
  });
  render(<InvitationsTab />, { wrapper: MemoryRouter });
  fireEvent.click(screen.getByTitle("Copy a new link without emailing"));
  await waitFor(() =>
    expect(mocks.resend).toHaveBeenCalledWith(
      expect.objectContaining({ id: "7", delivery: "link" }),
    ),
  );
  expect(
    await screen.findByText(
      `New link created for ${row.email}. Nothing was emailed; share this link with them yourself.`,
    ),
  ).toBeInTheDocument();
  expect(screen.getByText("https://example.invalid/invite/replaced")).toBeInTheDocument();
});
it("emails a new link only when asked and email is configured", async () => {
  mocks.resend.mockResolvedValue({
    invitation: row,
    claim_url: "https://example.invalid/invite/emailed",
    delivery_status: "sent",
  });
  const view = render(<InvitationsTab />, { wrapper: MemoryRouter });
  fireEvent.click(screen.getByTitle("Email a new link"));
  await waitFor(() =>
    expect(mocks.resend).toHaveBeenCalledWith(
      expect.objectContaining({ id: "7", delivery: "email" }),
    ),
  );
  view.unmount();
  mocks.emailDelivery = false;
  mocks.linkRow = true;
  render(<InvitationsTab />, { wrapper: MemoryRouter });
  expect(screen.queryByTitle("Email a new link")).toBeNull();
  expect(screen.getByTitle("Copy a new link without emailing")).toBeInTheDocument();
  // A link invitation has no address to email.
  expect(screen.getAllByTitle(/new link/)).toHaveLength(2);
  fireEvent.click(screen.getByTitle("Replace with a new link"));
  await waitFor(() =>
    expect(mocks.resend).toHaveBeenLastCalledWith(
      expect.objectContaining({ id: "8", delivery: "link" }),
    ),
  );
});
