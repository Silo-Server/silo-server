// @vitest-environment jsdom

import { render, screen, fireEvent, cleanup } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, expect, it, vi } from "vitest";

import InviteCodesTab from "./InviteCodesTab";

const mocks = vi.hoisted(() => ({
  useAdminServerSettings: vi.fn(),
  create: vi.fn(),
}));

vi.mock("@/hooks/queries/admin/inviteCodes", () => ({
  useAdminInviteCodes: () => ({ data: [], isLoading: false }),
  useCreateInviteCode: () => ({ mutate: mocks.create, isPending: false }),
  useUpdateInviteCode: () => ({ mutate: vi.fn(), isPending: false }),
  useTopUpInviteCode: () => ({ mutate: vi.fn(), isPending: false }),
  useDeleteInviteCode: () => ({ mutate: vi.fn(), isPending: false }),
}));

vi.mock("@/hooks/queries/admin/settings", () => ({
  useAdminServerSettings: (...args: unknown[]) => mocks.useAdminServerSettings(...args),
}));

function renderTab() {
  return render(
    <MemoryRouter>
      <InviteCodesTab />
    </MemoryRouter>,
  );
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});
it("opens and retains a generated code on plain HTTP without crypto.randomUUID", () => {
  const getRandomValues = vi.fn((bytes: Uint8Array) => {
    bytes.fill(0x12);
    return bytes;
  });
  vi.stubGlobal("crypto", { getRandomValues });
  mocks.useAdminServerSettings.mockReturnValue({ data: undefined });
  mocks.create.mockClear();
  renderTab();
  fireEvent.click(screen.getByRole("button", { name: "Create Code" }));
  expect(screen.getByPlaceholderText("e.g. BETA2026")).toHaveValue("1212121212124212");
  fireEvent.click(screen.getByRole("button", { name: "Create" }));
  fireEvent.click(screen.getByRole("button", { name: "Create" }));
  expect(getRandomValues).toHaveBeenCalledTimes(1);
  expect(mocks.create).toHaveBeenCalledTimes(2);
  expect(mocks.create.mock.calls[0]?.[0].code).toBe("1212121212124212");
  expect(mocks.create.mock.calls[1]?.[0].code).toBe("1212121212124212");
});
