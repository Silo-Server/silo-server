// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { OwnAppKeysPanel } from "./OwnAppKeysPanel";

const state = vi.hoisted(() => ({
  impersonating: false,
  primary: true,
  capability: "available",
  capabilityFailed: false,
  refetchCapability: vi.fn(),
  access: { list: true, manage: true, approve: true, sign_in_again: false },
  // Each `enabled` the session-access read was asked with.
  accessEnabled: [] as boolean[],
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
  useAppKeyCapability: () =>
    state.capabilityFailed
      ? { isSuccess: false, isError: true, isFetching: false, refetch: state.refetchCapability }
      : { isSuccess: true, isError: false, data: { state: state.capability } },
  useAppKeySessionAccess: (enabled: boolean) => {
    state.accessEnabled.push(enabled);
    return { isPending: false, isError: false, isSuccess: true, data: state.access };
  },
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

function mount(entry = "/settings/sessions") {
  return render(
    <MemoryRouter initialEntries={[entry]}>
      <OwnAppKeysPanel />
    </MemoryRouter>,
  );
}

function keysResult(isPending: boolean) {
  return {
    keys: {
      isPending,
      isError: false,
      data: isPending ? undefined : { person_key: null, devices: [waitingPhone] },
      refetch: vi.fn(),
    },
    preapprovals: { isSuccess: !isPending, data: [], refetch: vi.fn() },
    revokeDevice: mutation,
    deny: mutation,
    approve: mutation,
    withdraw: mutation,
  };
}

// The element each scrollIntoView call was made on (jsdom has none).
const scrolledTo: string[] = [];
Element.prototype.scrollIntoView = function (this: Element) {
  scrolledTo.push(this.id);
};

beforeEach(() => {
  state.impersonating = false;
  state.primary = true;
  state.capability = "available";
  state.capabilityFailed = false;
  state.access = { list: true, manage: true, approve: true, sign_in_again: false };
  state.accessEnabled = [];
  scrolledTo.length = 0;
  state.useOwnAppKeys.mockReturnValue(keysResult(false));
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

// An administrator acting as the account sees no panel at all, and the page
// asks the server for neither the session's access nor the list.
it("renders and asks nothing for an administrator acting as the account", () => {
  state.impersonating = true;
  mount();
  expect(screen.queryByRole("heading")).toBeNull();
  expect(state.accessEnabled.length).toBeGreaterThan(0);
  expect(state.accessEnabled.every((enabled) => !enabled)).toBe(true);
  expect(state.useOwnAppKeys).not.toHaveBeenCalled();
});

// A failed capability read must not make the panel vanish: it says so and
// offers to ask again.
it("offers a retry when the capability read fails", () => {
  state.capabilityFailed = true;
  mount();
  expect(screen.getByRole("heading", { name: "Silo app devices" })).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Retry" }));
  expect(state.refetchCapability).toHaveBeenCalledTimes(1);
  expect(state.accessEnabled.length).toBeGreaterThan(0);
  expect(state.accessEnabled.every((enabled) => !enabled)).toBe(true);
});

// A link to one device (from a session row or a dialog) must land on its
// row, which exists only once the list has loaded.
it("scrolls to a linked device once its row is on the page", () => {
  state.useOwnAppKeys.mockReturnValue(keysResult(true));
  const view = mount(`/settings/sessions#device-${waitingPhone.id}`);
  expect(scrolledTo).toEqual([]);
  state.useOwnAppKeys.mockReturnValue(keysResult(false));
  view.rerender(
    <MemoryRouter initialEntries={[`/settings/sessions#device-${waitingPhone.id}`]}>
      <OwnAppKeysPanel />
    </MemoryRouter>,
  );
  expect(scrolledTo).toEqual([`device-${waitingPhone.id}`]);
});

// A malformed hash must not take the page down to its error boundary.
it("ignores a hash that doesn't decode", () => {
  mount("/settings/sessions#device-%zz");
  expect(screen.getByRole("heading", { name: "Silo app devices" })).toBeTruthy();
  expect(scrolledTo).toEqual([]);
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
