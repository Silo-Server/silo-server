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
      "server.public_url",
      "server.lan_discovery",
      "branding.server_name",
      "branding.login_subtitle",
      "signup.enabled",
      "password_reset.self_service_enabled",
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

  it("shows the local network discovery toggle in its saved state", () => {
    useSettingsFormMock.mockReturnValue(makeForm({ "server.lan_discovery": "true" }));
    renderPage();

    expect(screen.getByRole("switch", { name: /Show on the local network/i })).toHaveAttribute(
      "aria-checked",
      "true",
    );
  });

  it("shows the self-service password reset toggle in its saved state", () => {
    useSettingsFormMock.mockReturnValue(
      makeForm({ "password_reset.self_service_enabled": "true" }),
    );
    renderPage();

    expect(screen.getByRole("switch", { name: /Self-service password reset/i })).toHaveAttribute(
      "aria-checked",
      "true",
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

  // The save bar moves to components/ but must render the same markup on every
  // settings page that uses it: scroll room, scrim and the pill.
  it("renders the settings save bar for a dirty form", () => {
    useSettingsFormMock.mockReturnValue({ ...makeForm(), dirtyCount: 2 });
    renderPage();

    const pill = screen.getByText("2 unsaved changes").closest("[role=status]");
    const scrim = pill?.previousElementSibling;
    expect([scrim?.previousElementSibling, scrim, pill]).toMatchInlineSnapshot(`
      [
        <div
          aria-hidden="true"
          class="h-28"
        />,
        <div
          aria-hidden="true"
          class="pointer-events-none fixed right-0 bottom-0 left-0 z-30 h-40 bg-gradient-to-t from-[var(--background)] via-[color-mix(in_srgb,var(--background)_72%,transparent)] to-transparent lg:left-[240px]"
        />,
        <div
          class="pointer-events-none fixed right-0 bottom-6 left-0 z-40 flex justify-center px-4 lg:left-[240px]"
          role="status"
        >
          <div
            class="glass pointer-events-auto flex max-w-full items-center gap-3 rounded-full py-2 pr-2 pl-4 shadow-2xl backdrop-blur-xl sm:gap-4 sm:pl-5"
          >
            <span
              class="min-w-0 truncate text-[13px] font-medium"
            >
              2 unsaved changes
            </span>
            <span
              class="flex shrink-0 items-center gap-1.5"
            >
              <button
                class="inline-flex shrink-0 items-center justify-center text-sm font-medium whitespace-nowrap transition-colors outline-none focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/50 disabled:pointer-events-none disabled:opacity-50 aria-invalid:border-destructive aria-invalid:ring-destructive/20 [&_svg]:pointer-events-none [&_svg]:shrink-0 [&_svg:not([class*='size-'])]:size-4 duration-150 hover:bg-accent hover:text-accent-foreground h-8 gap-1.5 px-3 has-[>svg]:px-2.5 rounded-full"
                data-size="sm"
                data-slot="button"
                data-variant="ghost"
              >
                Discard
              </button>
              <button
                class="inline-flex shrink-0 items-center justify-center text-sm font-medium whitespace-nowrap transition-colors outline-none focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/50 disabled:pointer-events-none disabled:opacity-50 aria-invalid:border-destructive aria-invalid:ring-destructive/20 [&_svg]:pointer-events-none [&_svg]:shrink-0 [&_svg:not([class*='size-'])]:size-4 shadow-sm duration-150 h-8 gap-1.5 px-3 has-[>svg]:px-2.5 rounded-full bg-[var(--settings-accent)] text-[#15151a] hover:bg-[var(--settings-accent)] hover:brightness-110"
                data-size="sm"
                data-slot="button"
                data-variant="default"
              >
                Save
              </button>
            </span>
          </div>
        </div>,
      ]
    `);
  });
});
