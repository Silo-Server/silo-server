// @vitest-environment jsdom
import { cleanup, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { OwnAppKeysPanel } from "./OwnAppKeysPanel";

const state = vi.hoisted(() => ({
  impersonating: false,
  primary: true,
  capability: "available",
  access: { list: true, manage: true, approve: true, sign_in_again: false },
  useOwnAppKeys: vi.fn(),
}));
vi.mock("@/hooks/useAuth", () => ({
  useAuth: () => ({
    isImpersonating: state.impersonating,
    profile: { id: "p1", name: "Sam", has_pin: false, is_primary: state.primary },
    selectProfile: vi.fn(),
    verifyProfilePin: vi.fn(),
    logoutOfSiloOnly: vi.fn(),
  }),
}));
vi.mock("@/hooks/queries/appKeySession", () => ({
  useAppKeyCapability: () => ({ isSuccess: true, data: { state: state.capability } }),
  useAppKeySessionAccess: () => ({ isPending: false, isError: false, data: state.access }),
}));
vi.mock("@/hooks/queries/ownAppKeys", () => ({ useOwnAppKeys: state.useOwnAppKeys }));

const waitingPhone = {
  id: "0c5f1a52-7e0c-4f0e-9f43-2f1f6f0c7d11",
  kid: "k1",
  kind: "device",
  name: "New phone",
  platform: "ios",
  status: "pending",
  enrolled_by: "introduce",
  allowed_by: null,
  enrolled_at: "2026-11-26T09:00:00Z",
  enrolled_ip_address: "",
  last_used_at: null,
};
const mutation = { isPending: false, mutateAsync: vi.fn() };

function mount() {
  render(
    <MemoryRouter>
      <OwnAppKeysPanel />
    </MemoryRouter>,
  );
}

beforeEach(() => {
  state.impersonating = false;
  state.primary = true;
  state.capability = "available";
  state.access = { list: true, manage: true, approve: true, sign_in_again: false };
  state.useOwnAppKeys.mockReturnValue({
    keys: {
      isPending: false,
      isError: false,
      data: { person_key: null, devices: [waitingPhone] },
      refetch: vi.fn(),
    },
    preapprovals: { isSuccess: true, data: [], refetch: vi.fn() },
    revokeDevice: mutation,
    deny: mutation,
    approve: mutation,
    withdraw: mutation,
  });
});
afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

// A session that can't list devices (a QR-code sign-in, or one older than the
// record of how sessions open) gets the notice, never an error, and the page
// doesn't ask for a list the server would refuse.
it.each([
  [true, "Sign in again to manage your devices"],
  [false, "Manage your devices in the Silo app."],
])("shows the notice when the session can't list devices (sign in again: %s)", (again, text) => {
  state.access = { list: false, manage: false, approve: false, sign_in_again: again };
  mount();
  expect(screen.getByRole("status")).toHaveTextContent(text);
  expect(state.useOwnAppKeys).not.toHaveBeenCalled();
});

// An administrator acting as the account sees no panel at all.
it("renders nothing for an administrator acting as the account", () => {
  state.impersonating = true;
  mount();
  expect(screen.queryByRole("heading")).toBeNull();
});

// Approve is offered only where it can work: the main profile, with app keys
// on. Elsewhere the person can still deny, and is told why Approve is missing.
it.each([
  { name: "main profile", primary: true, approve: true, capability: "available", shown: true },
  { name: "other profile", primary: false, approve: false, capability: "available", shown: false },
  { name: "app keys off", primary: true, approve: true, capability: "disabled", shown: false },
])("offers Approve on a waiting device only where it works ($name)", (row) => {
  state.primary = row.primary;
  state.capability = row.capability;
  state.access = { list: true, manage: true, approve: row.approve, sign_in_again: false };
  mount();
  expect(screen.getByRole("button", { name: "Deny New phone" })).toBeTruthy();
  expect(Boolean(screen.queryByRole("button", { name: "Approve New phone" }))).toBe(row.shown);
  expect(
    Boolean(
      screen.queryByText("Only the household’s main profile can approve a device.", {
        exact: false,
      }),
    ),
  ).toBe(!row.primary);
});
