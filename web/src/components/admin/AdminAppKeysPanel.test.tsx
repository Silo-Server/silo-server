// @vitest-environment jsdom
import { cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { AdminAppKeysPanel } from "./AdminAppKeysPanel";

const key = (id: string, name: string, extra: Record<string, unknown> = {}) => ({
  id,
  kid: id,
  kind: "device",
  name,
  platform: "ios",
  status: "active",
  enrolled_by: "introduce",
  allowed_by: null,
  enrolled_at: "2026-10-05T00:00:00Z",
  enrolled_ip_address: "",
  last_used_at: null,
  provider_bound: false,
  identity_gone: false,
  waiting_until: null,
  ...extra,
});
const mocks = vi.hoisted(() => ({ revokeDevice: vi.fn(), capability: "available" }));
const idle = { isPending: false, mutateAsync: vi.fn() };
vi.mock("@/hooks/queries/appKeys", () => ({
  useAppKeyCapability: () => ({ data: { state: mocks.capability } }),
}));
vi.mock("@/hooks/queries/admin/appKeys", () => ({
  useAdminAppKeys: () => ({
    keys: {
      isPending: false,
      isError: false,
      isFetching: false,
      refetch: vi.fn(),
      data: {
        person_key: {
          kid: "p",
          enrolled_at: "2026-10-01T00:00:00Z",
          enrolled_ip_address: "203.0.113.7",
          enrolled_by: "session",
        },
        devices: [
          key("waiting", "New iPad", { status: "pending", waiting_until: "2026-10-06T00:00:00Z" }),
          key("dead", "Old laptop", {
            provider_bound: true,
            identity_gone: true,
            enrolled_ip_address: "198.51.100.24",
          }),
        ],
      },
    },
    preapprovals: {
      isPending: false,
      isError: false,
      isFetching: false,
      refetch: vi.fn(),
      data: [
        {
          id: "pre",
          device_name: "Living room TV",
          device_platform: "tvos",
          created_at: "2026-10-05T00:00:00Z",
          expires_at: "2026-11-05T00:00:00Z",
        },
      ],
    },
    revokeDevice: { isPending: false, mutateAsync: mocks.revokeDevice },
    revokePersonKey: idle,
    deny: idle,
    withdraw: idle,
  }),
}));
beforeEach(() => mocks.revokeDevice.mockReset());
afterEach(cleanup);

// The server refuses an admin acting on another admin's or the Owner's keys;
// the panel must not offer buttons it would refuse.
it("offers no actions on an account the viewer may not change", () => {
  render(<AdminAppKeysPanel user={{ id: 2, username: "sam" }} manageable={false} />, {
    wrapper: MemoryRouter,
  });
  expect(screen.getByText("Old laptop")).toBeInTheDocument();
  expect(screen.queryAllByRole("button", { name: /Remove|Deny|Withdraw/ })).toHaveLength(0);
  expect(
    screen.getByText("Only the server owner can change another admin’s or the owner’s devices."),
  ).toBeInTheDocument();
});

// A key whose provider account is gone can't sign in, but it still belongs
// to the account until someone removes it.
it("still offers to remove a device that can no longer sign in", async () => {
  mocks.revokeDevice.mockResolvedValueOnce(undefined);
  const user = userEvent.setup();
  render(<AdminAppKeysPanel user={{ id: 2, username: "sam" }} manageable />, {
    wrapper: MemoryRouter,
  });
  await user.click(screen.getByRole("button", { name: "Remove Old laptop" }));
  await user.click(
    within(screen.getByRole("alertdialog")).getByRole("button", { name: "Remove device" }),
  );
  expect(mocks.revokeDevice).toHaveBeenCalledWith("dead");
});

// Where a key was added from sits beside when; a key added before the server
// recorded it shows no address rather than an empty "From".
it("shows the address each key was added from, and none when it is unknown", () => {
  const { container } = render(
    <AdminAppKeysPanel user={{ id: 2, username: "sam" }} manageable={false} />,
    { wrapper: MemoryRouter },
  );
  const shown = [...container.querySelectorAll("bdi")].map((el) => el.parentElement?.textContent);
  expect(shown).toHaveLength(2);
  expect(shown[0]).toBe("From 203.0.113.7");
  expect(shown[1]).toMatch(/ · From 198\.51\.100\.24$/);
});

// The footnote says a signed-out device signs back in with its key. That is
// false while app key sign-in is off, and for a disabled account, whose keys
// are refused at sign-in.
it.each([
  { name: "enabled account, app keys on", capability: "available", enabled: true, shown: true },
  { name: "app keys off", capability: "disabled", enabled: true, shown: false },
  { name: "disabled account", capability: "available", enabled: false, shown: false },
])("says devices sign back in only where they do ($name)", (row) => {
  mocks.capability = row.capability;
  render(<AdminAppKeysPanel user={{ id: 2, username: "sam", enabled: row.enabled }} manageable />, {
    wrapper: MemoryRouter,
  });
  expect(Boolean(screen.queryByText(/it signs back in with its key/))).toBe(row.shown);
  mocks.capability = "available";
});
