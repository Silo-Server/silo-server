import { render as renderDOM, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { PluginInstallation } from "@/api/types";

import WatchSyncSettings from "./WatchSyncSettings";

const mocks = vi.hoisted(() => ({
  toastInfo: vi.fn(),
  toastSuccess: vi.fn(),
  updateSettings: vi.fn(),
}));

let pluginInstallations: Partial<PluginInstallation>[] = [];

vi.mock("@/hooks/queries/admin/plugins", () => ({
  useAdminPluginInstallations: () => ({ data: pluginInstallations }),
}));

function render(ui: React.ReactElement) {
  return renderDOM(<MemoryRouter>{ui}</MemoryRouter>);
}

let sensitiveConfigured: string[] = ["watchsync.trakt.client_id", "watchsync.trakt.client_secret"];

const useSettingsFormMock = vi.fn((_options?: { keys: string[] }) => ({
  isLoading: false,
  getValue: () => "",
  setValue: vi.fn(),
  resetValue: vi.fn(),
  dirtyCount: 0,
  dirtyKeys: [],
  isDirty: vi.fn(() => false),
  save: vi.fn(),
  discard: vi.fn(),
  isSaving: false,
  restartRequired: false,
  sensitiveConfigured,
  sensitiveManagedByEnv: [],
  sensitiveStatusReady: true,
  sensitiveStatusError: false,
  buildConnectionCheckRequest: vi.fn(() => ({ values: {}, dirty_keys: [] })),
}));

vi.mock("@/hooks/useSettingsForm", () => ({
  useSettingsForm: (options: { keys: string[] }) => useSettingsFormMock(options),
}));

vi.mock("@/hooks/useRestartKeys", () => ({
  useRestartKeys: () => new Set<string>(),
}));

vi.mock("@/hooks/queries/admin/settings", () => ({
  useUpdateServerSettings: () => ({ mutateAsync: mocks.updateSettings, isPending: false }),
  useAdminServerStatus: () => ({ data: undefined }),
}));

vi.mock("sonner", () => ({
  toast: {
    info: mocks.toastInfo,
    success: mocks.toastSuccess,
  },
}));

describe("WatchSyncSettings", () => {
  beforeEach(() => {
    localStorage.clear();
    sensitiveConfigured = ["watchsync.trakt.client_id", "watchsync.trakt.client_secret"];
    pluginInstallations = [];
    for (const mock of Object.values(mocks)) mock.mockReset();
    mocks.updateSettings.mockResolvedValue({ values: {}, restart_required: false });
  });

  it("heads the page and lists both providers", () => {
    render(<WatchSyncSettings />);

    expect(screen.getByRole("heading", { level: 1, name: "Watch Providers" })).toBeInTheDocument();
    expect(
      screen.queryByText("Keep watch history in sync with Trakt and Simkl."),
    ).not.toBeInTheDocument();
    expect(screen.getByRole("group", { name: "Trakt" })).toBeInTheDocument();
    expect(screen.getByRole("group", { name: "Simkl" })).toBeInTheDocument();
  });

  it("registers only the watch sync app credentials with the settings form", () => {
    render(<WatchSyncSettings />);

    expect(useSettingsFormMock).toHaveBeenCalledWith({
      keys: [
        "watchsync.trakt.client_id",
        "watchsync.trakt.client_secret",
        "watchsync.simkl.client_id",
        "watchsync.simkl.client_secret",
      ],
    });
  });

  it("derives tile state from the stored credentials", () => {
    render(<WatchSyncSettings />);

    expect(screen.getByRole("group", { name: "Trakt" })).toHaveAttribute("data-state", "connected");
    expect(screen.getByRole("group", { name: "Simkl" })).toHaveAttribute(
      "data-state",
      "not_connected",
    );
    expect(screen.getByText("Connected")).toBeInTheDocument();
    expect(screen.getByText("Not connected")).toBeInTheDocument();
    // The state word is the only signal; the tile no longer repeats it as a
    // "credentials stored" line underneath.
    expect(screen.queryByText("App credentials stored")).not.toBeInTheDocument();
  });

  it("marks a half-configured provider as partly set up", () => {
    sensitiveConfigured = ["watchsync.simkl.client_id"];

    render(<WatchSyncSettings />);

    expect(screen.getByText("Partly set up")).toBeInTheDocument();
  });

  it("expands one tile in place and collapses it again", async () => {
    const user = userEvent.setup();
    render(<WatchSyncSettings />);

    expect(screen.queryByLabelText("Client ID")).not.toBeInTheDocument();

    await user.click(
      within(screen.getByRole("group", { name: "Simkl" })).getByRole("button", { name: "Connect" }),
    );

    const simkl = screen.getByRole("group", { name: "Simkl" });
    expect(simkl).toHaveAttribute("data-expanded", "true");
    expect(within(simkl).getByLabelText("Client ID")).toBeInTheDocument();
    expect(within(simkl).getByLabelText("Client secret")).toBeInTheDocument();
    expect(screen.getByRole("group", { name: "Trakt" })).not.toHaveAttribute("data-expanded");

    await user.click(within(simkl).getByRole("button", { name: "Close" }));

    expect(screen.queryByLabelText("Client ID")).not.toBeInTheDocument();
  });

  it("gives every panel action a resting affordance instead of ghost text", async () => {
    const user = userEvent.setup();
    render(<WatchSyncSettings />);

    await user.click(
      within(screen.getByRole("group", { name: "Trakt" })).getByRole("button", { name: "Manage" }),
    );

    const trakt = screen.getByRole("group", { name: "Trakt" });
    for (const name of ["Clear credentials", "Close"]) {
      expect(within(trakt).getByRole("button", { name })).toHaveAttribute(
        "data-variant",
        "outline",
      );
    }
  });

  it("saves only the credentials that were typed", async () => {
    const user = userEvent.setup();
    render(<WatchSyncSettings />);

    await user.click(
      within(screen.getByRole("group", { name: "Simkl" })).getByRole("button", { name: "Connect" }),
    );
    const simkl = screen.getByRole("group", { name: "Simkl" });
    await user.type(within(simkl).getByLabelText("Client ID"), "simkl-id");
    await user.click(within(simkl).getByRole("button", { name: "Save" }));

    expect(mocks.updateSettings).toHaveBeenCalledWith({ "watchsync.simkl.client_id": "simkl-id" });
  });

  it("lists installed watch-provider plugins beside the built-ins", () => {
    pluginInstallations = [
      {
        id: 7,
        plugin_id: "silo-plugin-watchsync-anilist",
        enabled: true,
        capabilities: [
          {
            type: "watch_sync_provider.v1",
            id: "anilist",
            display_name: "AniList",
          },
        ],
      },
      {
        id: 9,
        plugin_id: "silo-plugin-watchsync-serializd",
        enabled: false,
        capabilities: [
          {
            type: "watch_sync_provider.v1",
            id: "serializd",
            display_name: "Serializd",
          },
        ],
      },
      {
        // Not a watch provider; must not appear on this page.
        id: 11,
        plugin_id: "silo-plugin-metadata-tmdb",
        enabled: true,
        capabilities: [{ type: "metadata_provider.v1", id: "tmdb", display_name: "TMDB" }],
      },
    ];

    render(<WatchSyncSettings />);

    const anilist = screen.getByRole("group", { name: "AniList" });
    expect(anilist).toHaveAttribute("data-state", "connected");
    expect(within(anilist).getByText("Enabled")).toBeInTheDocument();
    expect(within(anilist).getByRole("button", { name: "Configure" })).toBeInTheDocument();

    const serializd = screen.getByRole("group", { name: "Serializd" });
    expect(serializd).toHaveAttribute("data-state", "not_connected");
    expect(within(serializd).getByText("Disabled")).toBeInTheDocument();

    expect(screen.queryByRole("group", { name: "TMDB" })).not.toBeInTheDocument();
    expect(screen.getByRole("link", { name: "plugin catalog" })).toHaveAttribute(
      "href",
      "/admin/plugins?tab=catalog",
    );
  });

  it("does not label an enabled plugin as connected until its required config is set", () => {
    const capability = {
      type: "watch_sync_provider.v1",
      id: "anilist",
      display_name: "AniList",
    };
    const schema = [{ key: "account", title: "Account", json_schema: "{}", required: true }];
    pluginInstallations = [
      {
        id: 7,
        plugin_id: "silo-plugin-watchsync-anilist",
        enabled: true,
        capabilities: [capability],
        global_config_schema: schema,
        global_configs: [],
      },
      {
        id: 9,
        plugin_id: "silo-plugin-watchsync-serializd",
        enabled: true,
        capabilities: [
          { type: "watch_sync_provider.v1", id: "serializd", display_name: "Serializd" },
        ],
        global_config_schema: schema,
        global_configs: [{ key: "account", value: {}, configured_secrets: ["api_key"] }],
      },
    ];

    render(<WatchSyncSettings />);

    // Enabled but keyless: the plugin cannot serve a request, so the tile must
    // not read as set up.
    const anilist = screen.getByRole("group", { name: "AniList" });
    expect(anilist).toHaveAttribute("data-state", "not_connected");
    expect(within(anilist).getByText("Needs setup")).toBeInTheDocument();

    const serializd = screen.getByRole("group", { name: "Serializd" });
    expect(serializd).toHaveAttribute("data-state", "connected");
    expect(within(serializd).getByText("Enabled")).toBeInTheDocument();
  });

  it("clears a connected provider behind a confirmation", async () => {
    const user = userEvent.setup();
    render(<WatchSyncSettings />);

    await user.click(
      within(screen.getByRole("group", { name: "Trakt" })).getByRole("button", { name: "Manage" }),
    );
    await user.click(
      within(screen.getByRole("group", { name: "Trakt" })).getByRole("button", {
        name: "Clear credentials",
      }),
    );

    expect(screen.getByText("Clear Trakt credentials?")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Clear" }));

    expect(mocks.updateSettings).toHaveBeenCalledWith({
      "watchsync.trakt.client_id": "",
      "watchsync.trakt.client_secret": "",
    });
  });
});
