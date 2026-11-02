import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

import CompatibilityProxiesSettings from "./CompatibilityProxiesSettings";
import { fakeSettingsForm } from "./settingsForm.test-support";

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

const makeForm = (overrides: Record<string, string> = {}, dirty: string[] = []) =>
  fakeSettingsForm({ ...DEFAULTS, ...overrides }, { dirty });

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
});
