// @vitest-environment jsdom

import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { describe, expect, it, vi } from "vitest";

import InviteCodesTab from "./InviteCodesTab";

const mocks = vi.hoisted(() => ({
  useAdminServerSettings: vi.fn(),
}));

vi.mock("@/hooks/queries/admin/inviteCodes", () => ({
  useAdminInviteCodes: () => ({ data: [], isLoading: false }),
  useCreateInviteCode: () => ({ mutate: vi.fn(), isPending: false }),
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

describe("InviteCodesTab public-signup status", () => {
  it("renders nothing signup-status related while settings are loading", () => {
    mocks.useAdminServerSettings.mockReturnValue({ data: undefined, isLoading: true });
    renderTab();

    expect(screen.queryByText(/public signups/i)).not.toBeInTheDocument();
  });

  it("shows a quiet caption when public signups are on", () => {
    mocks.useAdminServerSettings.mockReturnValue({
      data: { "signup.enabled": "true" },
      isLoading: false,
    });
    renderTab();

    expect(screen.getByText(/codes only work while public signups are on/i)).toBeInTheDocument();
    expect(screen.queryByText(/public signups are off/i)).not.toBeInTheDocument();
    const link = screen.getByRole("link", { name: /public signups setting/i });
    expect(link).toHaveAttribute("href", "/admin/settings/general");
  });

  it("promotes to a warning callout when public signups are off", () => {
    mocks.useAdminServerSettings.mockReturnValue({
      data: { "signup.enabled": "false" },
      isLoading: false,
    });
    renderTab();

    expect(screen.getByText(/public signups are off/i)).toBeInTheDocument();
    expect(screen.getByText(/these codes won't work until you enable them/i)).toBeInTheDocument();
    expect(
      screen.queryByText(/codes only work while public signups are on/i),
    ).not.toBeInTheDocument();
    const link = screen.getByRole("link", { name: /public signups setting/i });
    expect(link).toHaveAttribute("href", "/admin/settings/general");
  });
});
