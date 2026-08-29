// @vitest-environment jsdom

import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

import AdminUsers from "./AdminUsers";

// The page is exercised for its tab wiring only; the user table's data and the
// two invite tabs each own their own queries and tests.
vi.mock("@/hooks/queries/admin/users", () => ({
  useAdminUsers: () => ({ data: [], isLoading: false }),
  useCreateUser: () => ({ mutate: vi.fn(), isPending: false }),
  useUpdateUser: () => ({ mutate: vi.fn(), isPending: false }),
  useDeleteUser: () => ({ mutate: vi.fn(), isPending: false }),
}));

const mocks = vi.hoisted(() => ({
  useAdminServerSettings: vi.fn(),
}));

vi.mock("@/hooks/queries/admin/settings", () => ({
  useAdminServerSettings: (...args: unknown[]) => mocks.useAdminServerSettings(...args),
}));

vi.mock("@/hooks/queries/admin/libraries", () => ({
  useAdminLibraries: () => ({ data: [] }),
}));

vi.mock("@/hooks/queries/admin/accessGroups", () => ({
  useAccessGroups: () => ({ data: [] }),
}));

vi.mock("./admin-settings/InvitationsTab", () => ({
  default: () => <div>Invitations panel</div>,
}));

vi.mock("./admin-settings/InviteCodesTab", () => ({
  default: () => <div>Invite codes panel</div>,
}));

function LocationProbe() {
  const location = useLocation();
  return <span data-testid="location">{`${location.pathname}${location.search}`}</span>;
}

function renderPage(entry = "/admin/users") {
  return render(
    <MemoryRouter initialEntries={[entry]}>
      <Routes>
        <Route
          path="/admin/users"
          element={
            <>
              <AdminUsers />
              <LocationProbe />
            </>
          }
        />
      </Routes>
    </MemoryRouter>,
  );
}

function tab(name: string) {
  return screen.getByRole("tab", { name });
}

describe("AdminUsers tabs", () => {
  beforeEach(() => {
    mocks.useAdminServerSettings.mockReset();
    mocks.useAdminServerSettings.mockReturnValue({
      data: { "signup.enabled": "false" },
      isLoading: false,
    });
  });

  it("opens on the users tab when no tab is requested", () => {
    renderPage();

    expect(tab("Users")).toHaveAttribute("aria-selected", "true");
    expect(tab("Invite Codes")).toHaveAttribute("aria-selected", "false");
  });

  it("selects the Invite Codes tab from ?tab=invite-codes", () => {
    renderPage("/admin/users?tab=invite-codes");

    expect(tab("Invite Codes")).toHaveAttribute("aria-selected", "true");
    expect(screen.getByText("Invite codes panel")).toBeInTheDocument();
  });

  it("falls back to the users tab for an unknown tab id", () => {
    renderPage("/admin/users?tab=not-a-tab");

    expect(tab("Users")).toHaveAttribute("aria-selected", "true");
  });

  it("writes the selected tab to the URL and drops the param on the default tab", async () => {
    renderPage();

    await userEvent.click(tab("Invitations"));
    expect(screen.getByTestId("location")).toHaveTextContent("/admin/users?tab=invitations");

    await userEvent.click(tab("Users"));
    expect(screen.getByTestId("location")).toHaveTextContent("/admin/users");
    expect(screen.getByTestId("location")).not.toHaveTextContent("tab=");
  });
});

describe("AdminUsers public-signup status badge", () => {
  beforeEach(() => {
    mocks.useAdminServerSettings.mockReset();
  });

  it("shows a neutral 'off' badge linking to General settings when signups are disabled", () => {
    mocks.useAdminServerSettings.mockReturnValue({
      data: { "signup.enabled": "false" },
      isLoading: false,
    });
    renderPage();

    const badge = screen.getByText("Public signups off");
    expect(badge).toHaveAttribute("data-variant", "secondary");
    const link = badge.closest("a");
    expect(link).toHaveAttribute("href", "/admin/settings/general");
  });

  it("shows a positive 'on' badge linking to General settings when signups are enabled", () => {
    mocks.useAdminServerSettings.mockReturnValue({
      data: { "signup.enabled": "true" },
      isLoading: false,
    });
    renderPage();

    const badge = screen.getByText("Public signups on");
    expect(badge).toHaveAttribute("data-variant", "outline");
    const link = badge.closest("a");
    expect(link).toHaveAttribute("href", "/admin/settings/general");
  });

  it("renders no signup-status badge while settings are still loading", () => {
    mocks.useAdminServerSettings.mockReturnValue({ data: undefined, isLoading: true });
    renderPage();

    expect(screen.queryByText("Public signups on")).not.toBeInTheDocument();
    expect(screen.queryByText("Public signups off")).not.toBeInTheDocument();
  });

  it("stays visible regardless of which tab is active", async () => {
    mocks.useAdminServerSettings.mockReturnValue({
      data: { "signup.enabled": "true" },
      isLoading: false,
    });
    renderPage();

    expect(screen.getByText("Public signups on")).toBeInTheDocument();

    await userEvent.click(tab("Invite Codes"));
    expect(screen.getByText("Public signups on")).toBeInTheDocument();

    await userEvent.click(tab("Invitations"));
    expect(screen.getByText("Public signups on")).toBeInTheDocument();
  });
});
