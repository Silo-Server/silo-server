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
  impersonating: false,
  pending: false,
  capability: "available" as string | undefined,
}));
vi.mock("@/hooks/useAuth", () => ({
  useAuth: () => ({ clearLoginSession: mocks.clear, isImpersonating: mocks.impersonating }),
}));
vi.mock("@/hooks/queries/appKeySession", () => ({
  useAppKeyCapability: () => ({
    data: mocks.capability ? { state: mocks.capability } : undefined,
  }),
}));
// The Silo app sentence asks the server whether app keys are on; a marker
// stands in for it, so a test sees only whether the panel shows it.
vi.mock("@/components/appKeys/SignsBackInNote", () => ({
  SignsBackInNote: ({ to }: { to?: unknown }) => (
    <span>{to === undefined ? "Silo app sentence without link" : "Silo app sentence"}</span>
  ),
}));
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
    reducedIds: new Set(),
    currentSession: null,
    revoke: { isPending: mocks.pending, mutateAsync: mocks.revoke },
    revokeAll: { isPending: false, mutateAsync: mocks.all },
  }),
}));
beforeEach(() => {
  mocks.revoke.mockReset();
  mocks.all.mockReset();
  mocks.clear.mockReset();
  mocks.sessions = [listedSession];
  mocks.impersonating = false;
  mocks.pending = false;
  mocks.capability = "available";
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
// Demo mode: a visitor may sign this browser out, not the other visitors.
it("offers only this session's sign out when others may not be revoked", () => {
  mocks.sessions = [listedSession, { ...listedSession, id: "this", current: true }];
  render(
    <MemoryRouter>
      <LoginSessionsPanel manageOthers={false} />
    </MemoryRouter>,
  );
  expect(screen.getAllByText("Chrome on Windows")).toHaveLength(2);
  expect(screen.getByRole("button", { name: "Sign out this browser" })).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "Sign out Chrome on Windows" })).toBeNull();
});

// A disabled account's keys are refused at sign-in, so its devices don't
// sign back in: the administrator's view must not say they do.
it.each([
  [true, true],
  [false, false],
])("says devices sign back in only for an enabled account (enabled: %s)", (enabled, shown) => {
  render(
    <MemoryRouter>
      <LoginSessionsPanel adminUser={{ id: 2, username: "sam", enabled }} />
    </MemoryRouter>,
  );
  expect(Boolean(screen.queryByText("Silo app sentence"))).toBe(shown);
});

const keySession = { ...listedSession, id: "key", device_name: "Pixel 9", device_key_id: "k1" };
const MARK = "This device signs back in by itself";

// The row's mark says the same as the sentence: true only while app key
// sign-in is available, and in an administrator's view only for an enabled
// account. Unknown while the capability read loads or failed: hidden.
it.each([
  { name: "own, app keys on", capability: "available", adminUser: undefined, shown: true },
  { name: "own, app keys off", capability: "disabled", adminUser: undefined, shown: false },
  { name: "own, capability unknown", capability: undefined, adminUser: undefined, shown: false },
  {
    name: "admin, disabled account",
    capability: "available",
    adminUser: { id: 2, username: "sam", enabled: false },
    shown: false,
  },
])("marks a session a key opened only where it signs back in ($name)", (row) => {
  mocks.capability = row.capability;
  mocks.sessions = [keySession];
  render(
    <MemoryRouter>
      <LoginSessionsPanel adminUser={row.adminUser} />
    </MemoryRouter>,
  );
  expect(Boolean(screen.queryByText(MARK))).toBe(row.shown);
});

// A disabled account's sign-out dialogs (one session a key opened, and every
// session) must not say its devices sign back in.
it.each([
  ["Sign out Pixel 9", true],
  ["Sign out Pixel 9", false],
  ["Sign out everywhere", true],
  ["Sign out everywhere", false],
])(
  "%s says devices sign back in only for an enabled account (enabled: %s)",
  async (button, enabled) => {
    mocks.sessions = [keySession];
    const user = userEvent.setup();
    render(
      <MemoryRouter>
        <LoginSessionsPanel adminUser={{ id: 2, username: "sam", enabled }} />
      </MemoryRouter>,
    );
    await user.click(screen.getByRole("button", { name: button }));
    const dialog = screen.getByRole("alertdialog");
    expect(Boolean(within(dialog).queryByText("Silo app sentence"))).toBe(enabled);
  },
);

// An administrator acting as the account has no devices list to go to: the
// words stay, the links go.
it("keeps the words but drops the links while an administrator acts as the account", () => {
  mocks.impersonating = true;
  mocks.sessions = [keySession];
  render(
    <MemoryRouter>
      <LoginSessionsPanel />
    </MemoryRouter>,
  );
  expect(screen.getByText("Silo app sentence without link")).toBeTruthy();
  expect(screen.getByText(MARK).closest("a")).toBeNull();
});

// While the sign-out runs, its dialog must stay open for the result: the
// sentence's link, which would close it, becomes words.
it("drops the sentence's link while the sign-out runs", async () => {
  mocks.sessions = [keySession];
  const user = userEvent.setup();
  const view = render(
    <MemoryRouter>
      <LoginSessionsPanel />
    </MemoryRouter>,
  );
  await user.click(screen.getByRole("button", { name: "Sign out Pixel 9" }));
  const dialog = () => screen.getByRole("alertdialog");
  expect(within(dialog()).getByText("Silo app sentence")).toBeTruthy();
  mocks.pending = true;
  view.rerender(
    <MemoryRouter>
      <LoginSessionsPanel />
    </MemoryRouter>,
  );
  expect(within(dialog()).getByText("Silo app sentence without link")).toBeTruthy();
});
