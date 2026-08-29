import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

import GeneralSettings from "./GeneralSettings";

const useSettingsFormMock = vi.fn();

vi.mock("@/hooks/useSettingsForm", () => ({
  useSettingsForm: (...args: unknown[]) => useSettingsFormMock(...args),
}));

vi.mock("@/hooks/useRestartKeys", () => ({
  useRestartKeys: () => new Set<string>(["server.log_level"]),
}));

function makeForm(values: Record<string, string> = {}) {
  return {
    isLoading: false,
    getValue: (key: string) => values[key] ?? "",
    setValue: vi.fn(),
    isDirty: () => false,
    dirtyCount: 0,
    save: vi.fn(),
    discard: vi.fn(),
    isSaving: false,
    restartRequired: false,
    sensitiveConfigured: [],
    sensitiveManagedByEnv: [],
  };
}

function renderPage() {
  return render(
    <MemoryRouter>
      <GeneralSettings />
    </MemoryRouter>,
  );
}

describe("GeneralSettings", () => {
  beforeEach(() => {
    localStorage.clear();
    useSettingsFormMock.mockReset();
    useSettingsFormMock.mockReturnValue(makeForm({ "signup.enabled": "true" }));
  });

  it("renders every field group", () => {
    renderPage();

    for (const heading of ["Identity", "Access", "Logging"]) {
      expect(screen.getByRole("group", { name: heading })).toBeInTheDocument();
    }
  });

  it("opens with the title alone: no breadcrumb, lede, or status strip", () => {
    renderPage();

    expect(screen.getByRole("heading", { name: "General" })).toBeInTheDocument();
    expect(screen.queryByText("Server name: Silo")).not.toBeInTheDocument();
    expect(screen.queryByText(/Settings ›/)).not.toBeInTheDocument();
  });

  it("manages identity, signup and logging keys on one save bar", () => {
    renderPage();

    expect(useSettingsFormMock.mock.calls[0]?.[0]?.keys).toEqual([
      "branding.server_name",
      "branding.login_subtitle",
      "signup.enabled",
      "server.log_level",
      "server.log_quiet",
    ]);
  });

  it("shows the public signup toggle in its saved state and links to invite codes", () => {
    renderPage();

    expect(screen.getByRole("switch", { name: /Public signups/i })).toHaveAttribute(
      "aria-checked",
      "true",
    );
    // Deep-links straight to the Invite Codes tab rather than the Users tab.
    expect(screen.getByRole("link", { name: /Manage invite codes/i })).toHaveAttribute(
      "href",
      "/admin/users?tab=invite-codes",
    );
  });

  it("keeps quiet log prefixes behind the advanced disclosure", () => {
    renderPage();

    expect(screen.queryByLabelText("Quiet log prefixes")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Advanced/i })).toHaveAttribute(
      "aria-expanded",
      "false",
    );
  });

  it("forces the advanced disclosure open while a hidden field is dirty", () => {
    useSettingsFormMock.mockReturnValue({
      ...makeForm(),
      isDirty: (key: string) => key === "server.log_quiet",
    });

    renderPage();

    expect(screen.getByLabelText("Quiet log prefixes")).toBeInTheDocument();
  });
});
