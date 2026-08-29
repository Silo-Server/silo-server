import { render as renderDOM, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { MarkerProviderConfig, PluginInstallation } from "@/api/types";

import ProvidersSettings from "./ProvidersSettings";

const mocks = vi.hoisted(() => ({
  checkConnection: vi.fn(),
  toastError: vi.fn(),
  toastInfo: vi.fn(),
  toastSuccess: vi.fn(),
  updateProvider: vi.fn(),
  testProvider: vi.fn(),
  updateSettings: vi.fn(),
  updateMarkerProvider: vi.fn(),
  validateMarkerProvider: vi.fn(),
}));

function render(ui: React.ReactElement) {
  return renderDOM(<MemoryRouter>{ui}</MemoryRouter>);
}

function markerProvider(overrides: Partial<MarkerProviderConfig> = {}): MarkerProviderConfig {
  return {
    provider: "plugin:6:introdb",
    display_name: "TheIntroDB",
    source_type: "plugin",
    plugin_id: "silo.theintrodb",
    plugin_installation_id: 6,
    capability_id: "introdb",
    is_submitter: true,
    fetch_enabled: true,
    fetch_priority: 10,
    contribute_enabled: false,
    contribute_auto_local: false,
    contribute_min_confidence: 0.95,
    ...overrides,
  };
}

let markerProviders: MarkerProviderConfig[] = [];
let pluginInstallations: Partial<PluginInstallation>[] = [];

vi.mock("@/hooks/queries/admin/markers", () => ({
  useMarkerProviders: () => ({ data: { providers: markerProviders }, isLoading: false }),
  useUpdateMarkerProvider: () => ({ mutate: mocks.updateMarkerProvider, isPending: false }),
  useValidateMarkerProvider: () => ({ mutate: mocks.validateMarkerProvider, isPending: false }),
}));

vi.mock("@/hooks/queries/admin/plugins", () => ({
  useAdminPluginInstallations: () => ({ data: pluginInstallations }),
}));

let sensitiveConfigured: string[] = ["mdblist.api_key"];
let settingsValues: Record<string, string> = {};

const useSettingsFormMock = vi.fn((_options?: { keys: string[] }) => ({
  isLoading: false,
  getValue: (key: string) => settingsValues[key] ?? "",
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

const reportUnsavedMock = vi.fn();
vi.mock("@/hooks/useUnsavedChanges", () => ({
  useReportUnsavedChanges: (dirty: boolean) => reportUnsavedMock(dirty),
}));

vi.mock("@/hooks/useSettingsForm", () => ({
  useSettingsForm: (options: { keys: string[] }) => useSettingsFormMock(options),
}));

vi.mock("@/hooks/useRestartKeys", () => ({
  useRestartKeys: () => new Set<string>(),
}));

vi.mock("@/hooks/queries/admin/settings", () => ({
  useUpdateServerSettings: () => ({ mutateAsync: mocks.updateSettings, isPending: false }),
  useCheckAdminSettingsConnection: () => ({
    mutateAsync: mocks.checkConnection,
    isPending: false,
  }),
}));

vi.mock("@/hooks/queries/admin/subtitles", () => ({
  useSubtitleProviders: () => ({
    data: {
      providers: [
        {
          provider_name: "subdl",
          enabled: false,
          has_api_key: false,
          has_credentials: false,
          updated_at: "",
        },
        {
          provider_name: "opensubtitles",
          enabled: true,
          has_api_key: false,
          has_credentials: true,
          updated_at: "",
        },
        {
          provider_name: "subsource",
          enabled: false,
          has_api_key: true,
          has_credentials: false,
          updated_at: "",
        },
      ],
    },
    isLoading: false,
  }),
  useUpdateSubtitleProvider: () => ({ mutate: mocks.updateProvider, isPending: false }),
  useTestSubtitleProvider: () => ({ mutate: mocks.testProvider, isPending: false }),
}));

vi.mock("sonner", () => ({
  toast: {
    error: mocks.toastError,
    info: mocks.toastInfo,
    success: mocks.toastSuccess,
  },
}));

describe("ProvidersSettings", () => {
  beforeEach(() => {
    localStorage.clear();
    sensitiveConfigured = ["mdblist.api_key"];
    settingsValues = {};
    markerProviders = [];
    pluginInstallations = [];
    for (const mock of Object.values(mocks)) mock.mockReset();
  });

  it("heads the page and every provider group", () => {
    render(<ProvidersSettings />);

    expect(
      screen.getByRole("heading", { level: 1, name: "Subtitles & Metadata" }),
    ).toBeInTheDocument();
    expect(
      screen.queryByText("Where Silo fetches subtitles, artwork, and descriptions."),
    ).not.toBeInTheDocument();
    expect(screen.getByRole("group", { name: "Subtitle providers" })).toBeInTheDocument();
    expect(screen.getByRole("group", { name: "Metadata providers" })).toBeInTheDocument();
    expect(screen.getByRole("group", { name: "Marker providers" })).toBeInTheDocument();
    expect(screen.queryByText("Searched in order, top to bottom")).not.toBeInTheDocument();
  });

  it("shows one tile per provider, in search order", () => {
    render(<ProvidersSettings />);

    const tiles = ["OpenSubtitles", "SubDL", "SubSource", "MDBList"].map((name) =>
      screen.getByRole("group", { name }),
    );
    for (const tile of tiles) expect(tile).toBeInTheDocument();
    expect(tiles[0]?.compareDocumentPosition(tiles[1] as Node)).toBe(
      Node.DOCUMENT_POSITION_FOLLOWING,
    );
  });

  it("derives each tile state from the stored credentials", () => {
    render(<ProvidersSettings />);

    const openSubtitles = screen.getByRole("group", { name: "OpenSubtitles" });
    expect(openSubtitles).toHaveAttribute("data-state", "connected");
    expect(within(openSubtitles).getByText("Connected")).toBeInTheDocument();
    // The state word is the only signal: no "credentials stored" line repeating it.
    expect(within(openSubtitles).queryByText(/credentials stored/)).not.toBeInTheDocument();

    const subdl = screen.getByRole("group", { name: "SubDL" });
    expect(subdl).toHaveAttribute("data-state", "not_connected");
    expect(within(subdl).getByText("Not connected")).toBeInTheDocument();
    expect(within(subdl).getByRole("button", { name: "Connect" })).toBeInTheDocument();

    // Configured but switched off: not searched, so not "connected".
    const subsource = screen.getByRole("group", { name: "SubSource" });
    expect(subsource).toHaveAttribute("data-state", "not_connected");
    expect(within(subsource).getByText("Connected · off")).toBeInTheDocument();

    // MDBList's credential is a server setting, read from sensitive status.
    const mdblist = screen.getByRole("group", { name: "MDBList" });
    expect(mdblist).toHaveAttribute("data-state", "connected");
    expect(within(mdblist).getByRole("button", { name: "Manage" })).toBeInTheDocument();
  });

  it("counts a missing MDBList key as not connected", () => {
    sensitiveConfigured = [];

    render(<ProvidersSettings />);

    expect(screen.getByRole("group", { name: "MDBList" })).toHaveAttribute(
      "data-state",
      "not_connected",
    );
  });

  it("reports a credential draft to the unsaved-changes registry", async () => {
    const user = userEvent.setup();
    render(<ProvidersSettings />);
    reportUnsavedMock.mockClear();

    await user.click(
      within(screen.getByRole("group", { name: "OpenSubtitles" })).getByRole("button", {
        name: /Manage|Connect|Set up/,
      }),
    );
    await user.type(screen.getByLabelText(/Username/i), "user");

    // Tile drafts live outside useSettingsForm; the navigation guard and the
    // reload prompt only see them through this report.
    expect(reportUnsavedMock).toHaveBeenLastCalledWith(true);
  });

  it("expands one tile in place and collapses it again", async () => {
    const user = userEvent.setup();
    render(<ProvidersSettings />);

    expect(screen.queryByLabelText("API key")).not.toBeInTheDocument();

    await user.click(
      within(screen.getByRole("group", { name: "SubDL" })).getByRole("button", { name: "Connect" }),
    );

    const subdl = screen.getByRole("group", { name: "SubDL" });
    expect(subdl).toHaveAttribute("data-expanded", "true");
    expect(subdl).toHaveAttribute("data-state", "editing");
    expect(within(subdl).getByLabelText("API key")).toBeInTheDocument();
    expect(within(subdl).getByRole("button", { name: "Test connection" })).toBeInTheDocument();
    // Only one panel is open at a time.
    expect(screen.getByRole("group", { name: "SubSource" })).not.toHaveAttribute("data-expanded");

    await user.click(within(subdl).getByRole("button", { name: "Close" }));

    expect(screen.getByRole("group", { name: "SubDL" })).not.toHaveAttribute("data-expanded");
    expect(screen.queryByLabelText("API key")).not.toBeInTheDocument();
  });

  it("swaps the expanded panel when another tile is opened", async () => {
    const user = userEvent.setup();
    render(<ProvidersSettings />);

    await user.click(
      within(screen.getByRole("group", { name: "SubDL" })).getByRole("button", { name: "Connect" }),
    );
    await user.click(
      within(screen.getByRole("group", { name: "MDBList" })).getByRole("button", {
        name: "Manage",
      }),
    );

    expect(screen.getByRole("group", { name: "SubDL" })).not.toHaveAttribute("data-expanded");
    expect(screen.getByRole("group", { name: "MDBList" })).toHaveAttribute("data-expanded", "true");
  });

  it("saves a subtitle provider from its own panel", async () => {
    const user = userEvent.setup();
    render(<ProvidersSettings />);

    await user.click(
      within(screen.getByRole("group", { name: "SubDL" })).getByRole("button", { name: "Connect" }),
    );
    const subdl = screen.getByRole("group", { name: "SubDL" });
    await user.type(within(subdl).getByLabelText("API key"), "key-123");
    await user.click(within(subdl).getByRole("button", { name: "Save" }));

    expect(mocks.updateProvider).toHaveBeenCalledWith(
      { provider: "subdl", config: { enabled: false, api_key: "key-123" } },
      expect.anything(),
    );
  });

  it("surfaces a failed provider test on the tile itself", async () => {
    const user = userEvent.setup();
    mocks.testProvider.mockImplementation((_vars, options) => {
      options.onSuccess({ success: false, error: "401 — key rejected" });
    });
    render(<ProvidersSettings />);

    await user.click(
      within(screen.getByRole("group", { name: "SubSource" })).getByRole("button", {
        name: "Manage",
      }),
    );
    await user.click(
      within(screen.getByRole("group", { name: "SubSource" })).getByRole("button", {
        name: "Test connection",
      }),
    );
    await user.click(
      within(screen.getByRole("group", { name: "SubSource" })).getByRole("button", {
        name: "Close",
      }),
    );

    const subsource = screen.getByRole("group", { name: "SubSource" });
    expect(subsource).toHaveAttribute("data-state", "error");
    expect(within(subsource).getByRole("button", { name: "Fix" })).toBeInTheDocument();
    expect(within(subsource).getByText("401 — key rejected")).toBeInTheDocument();
  });

  it("gives every panel action a resting affordance instead of ghost text", async () => {
    const user = userEvent.setup();
    render(<ProvidersSettings />);

    await user.click(
      within(screen.getByRole("group", { name: "SubSource" })).getByRole("button", {
        name: "Manage",
      }),
    );

    const subsource = screen.getByRole("group", { name: "SubSource" });
    expect(within(subsource).getByRole("button", { name: "Test connection" })).toHaveAttribute(
      "data-variant",
      "secondary",
    );
    for (const name of ["Disconnect", "Close"]) {
      expect(within(subsource).getByRole("button", { name })).toHaveAttribute(
        "data-variant",
        "outline",
      );
    }
  });

  it("points metadata plugins at the plugins page instead of faking tiles", () => {
    render(<ProvidersSettings />);

    expect(screen.getByRole("link", { name: "Plugins" })).toHaveAttribute("href", "/admin/plugins");
    expect(screen.queryByRole("group", { name: "TMDB" })).not.toBeInTheDocument();
  });

  it("says so plainly when no marker provider plugin is installed", () => {
    render(<ProvidersSettings />);

    expect(screen.getByText(/No marker provider plugins are installed/)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "plugin catalog" })).toHaveAttribute(
      "href",
      "/admin/plugins?tab=catalog",
    );
  });

  it("counts a marker provider whose plugin has no saved key as not connected", () => {
    markerProviders = [markerProvider()];
    pluginInstallations = [
      {
        id: 6,
        plugin_id: "silo.theintrodb",
        enabled: true,
        global_config_schema: [
          { key: "account", title: "Account", json_schema: "{}", required: false },
        ],
        global_configs: [],
      },
    ];

    render(<ProvidersSettings />);

    const tile = screen.getByRole("group", { name: "TheIntroDB" });
    expect(tile).toHaveAttribute("data-state", "not_connected");
    expect(within(tile).getByText("Needs setup")).toBeInTheDocument();
    // The next step is the plugin's own page, so the tile does not offer to
    // "Manage" settings that cannot work yet.
    expect(within(tile).getByRole("button", { name: "Set up" })).toBeInTheDocument();
  });

  it("counts a configured, lookup-enabled marker provider as connected", () => {
    markerProviders = [markerProvider()];
    pluginInstallations = [
      {
        id: 6,
        plugin_id: "silo.theintrodb",
        enabled: true,
        global_config_schema: [
          { key: "account", title: "Account", json_schema: "{}", required: true },
        ],
        global_configs: [{ key: "account", value: {}, configured_secrets: ["api_key"] }],
      },
    ];

    render(<ProvidersSettings />);

    const tile = screen.getByRole("group", { name: "TheIntroDB" });
    expect(tile).toHaveAttribute("data-state", "connected");
    expect(within(tile).getByText("Connected")).toBeInTheDocument();
  });

  it("marks a configured provider that is off for lookup", () => {
    markerProviders = [markerProvider({ fetch_enabled: false })];
    pluginInstallations = [
      {
        id: 6,
        plugin_id: "silo.theintrodb",
        enabled: true,
        global_config_schema: [
          { key: "account", title: "Account", json_schema: "{}", required: true },
        ],
        global_configs: [{ key: "account", value: {}, configured_secrets: ["api_key"] }],
      },
    ];

    render(<ProvidersSettings />);

    const tile = screen.getByRole("group", { name: "TheIntroDB" });
    expect(tile).toHaveAttribute("data-state", "not_connected");
    expect(within(tile).getByText("Connected · off")).toBeInTheDocument();
  });

  it("edits marker provider behavior in the tile and sends the whole row", async () => {
    const user = userEvent.setup();
    markerProviders = [markerProvider()];

    render(<ProvidersSettings />);

    await user.click(
      within(screen.getByRole("group", { name: "TheIntroDB" })).getByRole("button", {
        name: "Manage",
      }),
    );

    const tile = screen.getByRole("group", { name: "TheIntroDB" });
    expect(tile).toHaveAttribute("data-expanded", "true");
    expect(within(tile).getByLabelText("Lookup order")).toHaveValue(10);
    // Credentials are the plugin's, not Silo's: the panel links out for them.
    expect(within(tile).getByRole("link", { name: "plugin page" })).toHaveAttribute(
      "href",
      "/admin/plugins?installed_q=silo.theintrodb&configure=silo.theintrodb",
    );

    await user.clear(within(tile).getByLabelText("Lookup order"));
    await user.type(within(tile).getByLabelText("Lookup order"), "5");
    await user.click(within(tile).getByRole("button", { name: "Save" }));

    expect(mocks.updateMarkerProvider).toHaveBeenCalledWith({
      provider: "plugin:6:introdb",
      patch: {
        fetch_enabled: true,
        fetch_priority: 5,
        contribute_enabled: false,
        contribute_auto_local: false,
        contribute_min_confidence: 0.95,
      },
    });
  });

  it("admits when the detection mode never looks online", () => {
    markerProviders = [markerProvider()];
    settingsValues = { "markers.mode": "local" };

    render(<ProvidersSettings />);

    expect(screen.getByText(/Nothing here is searched right now/)).toBeInTheDocument();
    expect(screen.getByText(/is set to Detect on this server/)).toBeInTheDocument();
  });

  it("says when providers are in play instead", () => {
    markerProviders = [markerProvider()];
    settingsValues = { "markers.mode": "both" };

    render(<ProvidersSettings />);

    expect(screen.getByText(/Providers are searched when/)).toBeInTheDocument();
    expect(screen.queryByText(/Nothing here is searched right now/)).not.toBeInTheDocument();
  });

  it("closes a subtitle panel when a marker tile is opened", async () => {
    const user = userEvent.setup();
    markerProviders = [markerProvider()];

    render(<ProvidersSettings />);

    await user.click(
      within(screen.getByRole("group", { name: "SubDL" })).getByRole("button", { name: "Connect" }),
    );
    await user.click(
      within(screen.getByRole("group", { name: "TheIntroDB" })).getByRole("button", {
        name: "Manage",
      }),
    );

    expect(screen.getByRole("group", { name: "SubDL" })).not.toHaveAttribute("data-expanded");
    expect(screen.getByRole("group", { name: "TheIntroDB" })).toHaveAttribute(
      "data-expanded",
      "true",
    );
  });
});
