import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

import CompatibilityProxiesSettings from "./CompatibilityProxiesSettings";

const useSettingsFormMock = vi.fn();

vi.mock("@/hooks/useSettingsForm", () => ({
  useSettingsForm: (...args: unknown[]) => useSettingsFormMock(...args),
}));

vi.mock("@/hooks/useRestartKeys", () => ({
  useRestartKeys: () => new Set<string>(),
}));

let compatStatus: Record<string, unknown> = {};

vi.mock("@/hooks/queries/admin/settings", () => ({
  useJellyfinCompatStatus: () => ({ data: compatStatus, isLoading: false }),
  useInstallJellyfinCompatWeb: () => ({ mutate: vi.fn(), isPending: false }),
  useRemoveJellyfinCompatWeb: () => ({ mutate: vi.fn(), isPending: false }),
  useUpdateJellyfinCompatSettings: () => ({ mutate: vi.fn(), isPending: false }),
}));

const DEFAULTS: Record<string, string> = {
  "jellyfin_compat.enabled": "true",
  "jellyfin_compat.public_url": "https://media.example.com",
  "jellyfin_compat.server_name": "Silo",
  "jellyfin_compat.server_id": "4f1c0f8e2b6d4c1a9e3f7a2b5c8d0e1f",
  "jellyfin_compat.emulated_server_version": "12.1.0",
  "jellyfin_compat.session_ttl": "87600h",
  "jellyfin_compat.playback_session_ttl": "6h",
  "jellyfin_compat.web_enabled": "true",
  "jellyfin_compat.web_version": "12.1",
  "jellyfin_compat.web_install_dir": "/var/lib/silo/jellyfin-web",
  "audiobookshelf_compat.enabled": "false",
};

function makeForm(overrides: Record<string, string> = {}, dirty: string[] = []) {
  const values = { ...DEFAULTS, ...overrides };
  return {
    isLoading: false,
    getValue: (key: string) => values[key] ?? "",
    setValue: vi.fn(),
    isDirty: (key: string) => dirty.includes(key),
    dirtyKeys: dirty,
    dirtyCount: dirty.length,
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
      <CompatibilityProxiesSettings />
    </MemoryRouter>,
  );
}

const LOOPBACK_WARNING =
  "Only this server can reach that address. Use one other devices can reach.";

describe("CompatibilityProxiesSettings", () => {
  beforeEach(() => {
    localStorage.clear();
    compatStatus = { enabled: true, web_enabled: true, web_state: "missing", api_state: "running" };
    useSettingsFormMock.mockReset();
    useSettingsFormMock.mockReturnValue(makeForm());
  });

  it("shows the server ID read-only, saying what changing it does", async () => {
    renderPage();
    const jellyfin = screen.getByRole("group", { name: "Jellyfin" });
    await userEvent.click(within(jellyfin).getByRole("button", { name: "Advanced · 7 settings" }));

    expect(within(jellyfin).getByText(DEFAULTS["jellyfin_compat.server_id"]!)).toBeInTheDocument();
    expect(
      within(jellyfin).getByText(
        "Read-only. Changing it makes every Jellyfin app see a new server.",
      ),
    ).toBeInTheDocument();
    expect(within(jellyfin).queryByRole("textbox", { name: "Server ID" })).not.toBeInTheDocument();
    // Its key still loads so the value can be shown.
    expect(useSettingsFormMock.mock.calls[0]?.[0]?.keys).toContain("jellyfin_compat.server_id");
  });

  it("keeps the advanced Jellyfin fields editable", async () => {
    const form = makeForm();
    useSettingsFormMock.mockReturnValue(form);
    renderPage();
    await userEvent.click(screen.getByRole("button", { name: "Advanced · 7 settings" }));

    await userEvent.type(screen.getByLabelText("Name shown to Jellyfin apps"), "x");
    expect(form.setValue).toHaveBeenCalledWith("jellyfin_compat.server_name", "Silox");
    // Nothing reads branding.server_name for Jellyfin apps.
    expect(screen.getByText("Separate from the server name on General.")).toBeInTheDocument();
    // The default refills an emptied field, so "leave blank" is not offered.
    expect(screen.queryByText(/Leave blank/)).not.toBeInTheDocument();
    expect(
      screen.getByText(
        "The default pairs with the default reported Jellyfin version; change them together.",
      ),
    ).toBeInTheDocument();
  });

  it.each([
    "http://127.0.0.1:8096",
    "http://localhost:8096",
    "http://[::1]:8096",
    "127.0.0.2",
    "HTTP://LOCALHOST",
  ])("warns that %s only reaches the server itself", (address) => {
    useSettingsFormMock.mockReturnValue(makeForm({ "jellyfin_compat.public_url": address }));
    renderPage();

    expect(screen.getByText(LOOPBACK_WARNING)).toBeInTheDocument();
  });

  it.each([
    "https://media.example.com",
    "http://192.168.1.10:8096",
    "http://localhost.example.com",
    "",
  ])("does not warn about %j", (address) => {
    useSettingsFormMock.mockReturnValue(makeForm({ "jellyfin_compat.public_url": address }));
    renderPage();

    expect(screen.queryByText(LOOPBACK_WARNING)).not.toBeInTheDocument();
  });

  it("does not warn about the address while Jellyfin apps are not allowed", () => {
    useSettingsFormMock.mockReturnValue(
      makeForm({
        "jellyfin_compat.enabled": "false",
        "jellyfin_compat.public_url": "http://127.0.0.1:8096",
      }),
    );
    renderPage();

    expect(screen.queryByText(LOOPBACK_WARNING)).not.toBeInTheDocument();
  });

  it("labels Audiobookshelf as beta", () => {
    renderPage();

    const group = screen.getByRole("group", { name: "Audiobookshelf" });
    expect(within(group).getByText("Beta")).toBeInTheDocument();
    expect(
      within(group).getByRole("switch", { name: "Allow Audiobookshelf apps to connect" }),
    ).toBeInTheDocument();
    expect(within(screen.getByRole("group", { name: "Jellyfin" })).queryByText("Beta")).toBeNull();
  });

  it("opens the web player's download details through the shared disclosure", async () => {
    renderPage();

    const details = screen.getByRole("button", { name: "Download details" });
    expect(details).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByText("Listen address")).not.toBeInTheDocument();
    await userEvent.click(details);
    expect(screen.getByText("Listen address")).toBeInTheDocument();
  });
});
