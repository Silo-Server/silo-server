// @vitest-environment jsdom
import { cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { LoginSessionsPanel } from "./LoginSessionsPanel";
import {
  sessionEndReason,
  markSessionEnded,
  clearSignedOut,
  wasSignedOut,
} from "@/lib/externalSignIn";

const listedSession = {
  id: "one",
  device_name: "Chrome/1 Windows",
  created_at: "2026-10-05T00:00:00Z",
  expires_at: "2026-11-05T00:00:00Z",
  ip_address: "",
  last_seen_at: null,
  current: false,
};
const mocks = vi.hoisted(() => ({
  revoke: vi.fn(),
  all: vi.fn(),
  clear: vi.fn(),
  sessions: [] as unknown[],
}));
vi.mock("@/hooks/useAuth", () => ({ useAuth: () => ({ clearLoginSession: mocks.clear }) }));
vi.mock("@/hooks/queries/loginSessions", () => ({
  useLoginSessions: () => ({
    query: {
      isPending: false,
      isError: false,
      isFetching: false,
      hasNextPage: false,
      refetch: vi.fn(),
    },
    sessions: mocks.sessions,
    currentSession: null,
    revoke: { isPending: false, mutateAsync: mocks.revoke },
    revokeAll: { isPending: false, mutateAsync: mocks.all },
  }),
}));
beforeEach(() => {
  mocks.revoke.mockReset();
  mocks.all.mockReset();
  mocks.clear.mockReset();
  mocks.sessions = [listedSession];
  sessionStorage.clear();
});
afterEach(cleanup);
it("keeps failed revocation open and requires an explicit retry", async () => {
  mocks.revoke
    .mockRejectedValueOnce(new Error("Server unavailable"))
    .mockResolvedValueOnce(undefined);
  const user = userEvent.setup();
  render(
    <MemoryRouter>
      <LoginSessionsPanel />
    </MemoryRouter>,
  );
  await user.click(screen.getByRole("button", { name: "Sign out Chrome on Windows" }));
  await user.click(
    within(screen.getByRole("alertdialog")).getByRole("button", { name: "Sign out" }),
  );
  expect(await screen.findByRole("alert")).toHaveTextContent("Server unavailable");
  expect(screen.getByRole("alertdialog")).toBeInTheDocument();
  expect(mocks.revoke).toHaveBeenCalledTimes(1);
  await user.click(
    within(screen.getByRole("alertdialog")).getByRole("button", { name: "Sign out" }),
  );
  expect(mocks.revoke).toHaveBeenCalledTimes(2);
  expect(mocks.clear).not.toHaveBeenCalled();
});
it("names the selected account before revoking every session", async () => {
  const user = userEvent.setup();
  render(
    <MemoryRouter>
      <LoginSessionsPanel adminUser={{ id: 7, username: "admin2" }} />
    </MemoryRouter>,
  );
  await user.click(screen.getByRole("button", { name: "Sign out everywhere" }));
  const dialog = screen.getByRole("alertdialog");
  expect(dialog).toHaveTextContent("Sign admin2 out everywhere?");
  expect(dialog).toHaveTextContent("other accounts stay signed in");
  await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
  expect(mocks.all).not.toHaveBeenCalled();
});
it("offers sign out everywhere when no native session is listed", async () => {
  mocks.sessions = [];
  mocks.all.mockResolvedValueOnce(undefined);
  const user = userEvent.setup();
  render(
    <MemoryRouter>
      <LoginSessionsPanel adminUser={{ id: 7, username: "admin2" }} />
    </MemoryRouter>,
  );
  expect(screen.getByText("No active sign-ins for this account.")).toBeInTheDocument();
  await user.click(screen.getByRole("button", { name: "Sign out everywhere" }));
  await user.click(
    within(screen.getByRole("alertdialog")).getByRole("button", { name: "Sign out everywhere" }),
  );
  expect(mocks.all).toHaveBeenCalledTimes(1);
});
it("does not offer mutations for a protected account", () => {
  render(
    <MemoryRouter>
      <LoginSessionsPanel adminUser={{ id: 7, username: "owner" }} manageable={false} />
    </MemoryRouter>,
  );
  expect(screen.queryByRole("button", { name: /Sign out/ })).not.toBeInTheDocument();
  expect(screen.getByText("Chrome on Windows")).toBeInTheDocument();
});
it("keeps recovery on the sign-in form until a new sign-in clears it", () => {
  markSessionEnded("ended");
  // An expired or revoked session may still go back through the provider.
  expect(wasSignedOut()).toBe(false);
  expect(sessionEndReason()).toBe("ended");
  markSessionEnded("signed-out");
  expect(wasSignedOut()).toBe(true);
  expect(sessionEndReason()).toBe("signed-out");
  clearSignedOut();
  expect(wasSignedOut()).toBe(false);
  expect(sessionEndReason()).toBeNull();
});
