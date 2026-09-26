import { setAccessToken, setProfileId, setProfileToken } from "@/api/client";
import { adminSubtitleListScope } from "@/api/v2/adminSubtitles";
import { render as renderDOM, screen, within, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

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
    plugin_installation_id: "6",
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
    scope: adminSubtitleListScope(),
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

afterEach(() => vi.unstubAllGlobals());
describe("ProvidersSettings", () => {
  beforeEach(() => {
    localStorage.clear();
    setAccessToken("admin");
    setProfileId("owner");
    setProfileToken(null);
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>().mockImplementation(async (input) => {
        const provider = String(input).split("/").pop();
        return new Response(
          JSON.stringify({
            provider_name: provider,
            enabled: provider === "opensubtitles",
            has_api_key: provider === "subsource",
            has_credentials: provider === "opensubtitles",
          }),
          { headers: { "Content-Type": "application/json", ETag: '"captured"' } },
        );
      }),
    );
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
      {
        editor: expect.objectContaining({
          etag: '"captured"',
          intent: expect.objectContaining({ provider: "subdl" }),
        }),
        config: { enabled: false, api_key: "key-123" },
      },
      expect.anything(),
    );
  });

  it("retains subtitle draft after uncertain save and requires explicit reload", async () => {
    const user = userEvent.setup();
    mocks.updateProvider.mockImplementation((_vars, options) =>
      options.onError(new Error("lost response")),
    );
    render(<ProvidersSettings />);
    await user.click(
      within(screen.getByRole("group", { name: "SubDL" })).getByRole("button", { name: "Connect" }),
    );
    const panel = screen.getByRole("group", { name: "SubDL" });
    await user.type(within(panel).getByLabelText("API key"), "retained-secret");
    await user.click(within(panel).getByRole("button", { name: "Save" }));
    expect(within(panel).getByLabelText("API key")).toHaveValue("retained-secret");
    expect(within(panel).getByText(/Save not confirmed/)).toBeInTheDocument();
    expect(within(panel).getByRole("button", { name: "Save" })).toBeDisabled();
    expect(mocks.updateProvider).toHaveBeenCalledTimes(1);
    await user.click(
      within(panel).getByRole("button", { name: "Reload saved configuration (discard draft)" }),
    );
    await waitFor(() => expect(within(panel).getByRole("button", { name: "Save" })).toBeEnabled());
    expect(within(panel).getByLabelText("API key")).toHaveValue("");
    expect(globalThis.fetch).toHaveBeenCalledTimes(2);
  });

  it("shows subtitle saved with local failure without claiming immediate removal", async () => {
    const user = userEvent.setup();
    mocks.updateProvider.mockImplementation((_vars, options) =>
      options.onSuccess({ saved_revision: "5", local_apply: "failed" }),
    );
    render(<ProvidersSettings />);
    await user.click(
      within(screen.getByRole("group", { name: "SubSource" })).getByRole("button", {
        name: "Manage",
      }),
    );
    const panel = screen.getByRole("group", { name: "SubSource" });
    await user.click(within(panel).getByRole("button", { name: "Disconnect" }));
    expect(
      screen.getByText(/Applying this change on this server is reported separately/),
    ).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Clear and turn off" }));
    expect(mocks.updateProvider).toHaveBeenCalledWith(
      {
        editor: expect.objectContaining({ etag: '"captured"' }),
        config: { enabled: false, clear_credentials: true },
      },
      expect.anything(),
    );
    expect(within(panel).getByText(/Settings saved, but not applied/)).toBeInTheDocument();
    expect(within(panel).getByRole("button", { name: "Save" })).toBeDisabled();
  });

  it("does not enable the subtitle editor when authority changes during canonical load", async () => {
    let finish!: (r: Response) => void;
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation(
        () =>
          new Promise((resolve) => {
            finish = resolve;
          }),
      ),
    );
    const user = userEvent.setup();
    render(<ProvidersSettings />);
    await user.click(
      within(screen.getByRole("group", { name: "SubDL" })).getByRole("button", { name: "Connect" }),
    );
    setProfileToken("changed");
    finish(
      new Response(
        JSON.stringify({
          provider_name: "subdl",
          enabled: false,
          has_api_key: false,
          has_credentials: false,
        }),
        { headers: { "Content-Type": "application/json", ETag: '"v4"' } },
      ),
    );
    await waitFor(() =>
      expect(screen.getByText(/Unable to load saved configuration/)).toBeInTheDocument(),
    );
    expect(
      within(screen.getByRole("group", { name: "SubDL" })).getByRole("button", { name: "Save" }),
    ).toBeDisabled();
    expect(mocks.updateProvider).not.toHaveBeenCalled();
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
    expect(within(tile).getByLabelText("Provider priority")).toHaveValue(10);
    expect(within(tile).getByLabelText("Minimum confidence for automatic sharing")).toHaveValue(95);
    // Credentials are the plugin's, not Silo's: the panel links out for them.
    expect(within(tile).getByRole("link", { name: "plugin page" })).toHaveAttribute(
      "href",
      "/admin/plugins?installed_q=silo.theintrodb&configure=silo.theintrodb",
    );

    await user.clear(within(tile).getByLabelText("Provider priority"));
    await user.type(within(tile).getByLabelText("Provider priority"), "5");
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

  it.each(["off", "local"])("shows online lookup as disabled in %s mode", (mode) => {
    markerProviders = [markerProvider()];
    settingsValues = { "markers.mode": mode };

    render(<ProvidersSettings />);

    expect(screen.getByText(/Online marker lookup is disabled/)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Marker source" })).toHaveAttribute(
      "href",
      "/admin/settings/library",
    );
  });

  it.each(["online", "both", ""])("shows online lookup as enabled in %s mode", (mode) => {
    markerProviders = [markerProvider()];
    settingsValues = { "markers.mode": mode };

    render(<ProvidersSettings />);

    expect(screen.getByText(/Online marker lookup is enabled/)).toBeInTheDocument();
  });

  it.each([0, 92.5, 100])(
    "saves a confidence of %s percent in the API's 0–1 range",
    async (percent) => {
      const user = userEvent.setup();
      markerProviders = [markerProvider({ contribute_enabled: true, contribute_auto_local: true })];
      render(<ProvidersSettings />);
      const tile = within(screen.getByRole("group", { name: "TheIntroDB" }));
      await user.click(tile.getByRole("button", { name: "Manage" }));

      const confidence = tile.getByLabelText("Minimum confidence for automatic sharing");
      await user.clear(confidence);
      await user.type(confidence, String(percent));
      await user.click(tile.getByRole("button", { name: "Save" }));

      expect(mocks.updateMarkerProvider).toHaveBeenCalledWith({
        provider: "plugin:6:introdb",
        patch: {
          fetch_enabled: true,
          fetch_priority: 10,
          contribute_enabled: true,
          contribute_auto_local: true,
          contribute_min_confidence: percent / 100,
        },
      });
    },
  );

  it.each([0.947, 0.9500000000000001, 0.9876543210987654])(
    "keeps custom confidence %s clean and exact when another setting changes",
    async (savedConfidence) => {
      const user = userEvent.setup();
      markerProviders = [markerProvider({ contribute_min_confidence: savedConfidence })];
      reportUnsavedMock.mockClear();
      render(<ProvidersSettings />);
      const tile = within(screen.getByRole("group", { name: "TheIntroDB" }));
      await user.click(tile.getByRole("button", { name: "Manage" }));

      expect(tile.getByRole("button", { name: "Save" })).toBeDisabled();
      expect(reportUnsavedMock.mock.calls.every(([dirty]) => dirty === false)).toBe(true);

      await user.clear(tile.getByLabelText("Provider priority"));
      await user.type(tile.getByLabelText("Provider priority"), "5");
      await user.click(tile.getByRole("button", { name: "Save" }));

      expect(mocks.updateMarkerProvider).toHaveBeenCalledWith(
        expect.objectContaining({
          patch: expect.objectContaining({ contribute_min_confidence: savedConfidence }),
        }),
      );
    },
  );

  it.each(["", "-1", "101", "1e999"])("rejects invalid confidence %j", async (value) => {
    const user = userEvent.setup();
    markerProviders = [markerProvider({ contribute_enabled: true, contribute_auto_local: true })];
    render(<ProvidersSettings />);
    const tile = within(screen.getByRole("group", { name: "TheIntroDB" }));
    await user.click(tile.getByRole("button", { name: "Manage" }));

    const confidence = tile.getByLabelText("Minimum confidence for automatic sharing");
    await user.clear(confidence);
    if (value) await user.type(confidence, value);

    expect(confidence).toHaveAttribute("aria-invalid", "true");
    expect(tile.getByRole("button", { name: "Save" })).toBeDisabled();
    expect(mocks.updateMarkerProvider).not.toHaveBeenCalled();
  });

  it("enables sharing controls in order and retains confidence when sharing is turned off", async () => {
    const user = userEvent.setup();
    markerProviders = [markerProvider({ contribute_min_confidence: 0.947 })];
    render(<ProvidersSettings />);
    const tile = within(screen.getByRole("group", { name: "TheIntroDB" }));
    await user.click(tile.getByRole("button", { name: "Manage" }));

    const sharing = tile.getByRole("switch", { name: "Allow sharing with this provider" });
    const automatic = tile.getByRole("switch", { name: "Automatically share detected intros" });
    const confidence = tile.getByLabelText("Minimum confidence for automatic sharing");
    expect(automatic).toBeDisabled();
    expect(confidence).toBeEnabled();
    expect(confidence).toHaveValue(94.7);

    await user.click(sharing);
    expect(automatic).toBeEnabled();
    expect(confidence).toBeEnabled();
    await user.click(automatic);
    expect(confidence).toBeEnabled();
    await user.click(sharing);
    expect(automatic).not.toBeChecked();
    expect(automatic).toBeDisabled();
    expect(confidence).toBeEnabled();
    expect(confidence).toHaveValue(94.7);
    expect(tile.getByRole("button", { name: "Save" })).toBeDisabled();
  });

  it("keeps invalid drafts editable when lookup and sharing are turned off", async () => {
    const user = userEvent.setup();
    markerProviders = [markerProvider({ contribute_enabled: true, contribute_auto_local: true })];
    render(<ProvidersSettings />);
    const tile = within(screen.getByRole("group", { name: "TheIntroDB" }));
    await user.click(tile.getByRole("button", { name: "Manage" }));
    const priority = tile.getByLabelText("Provider priority");
    const confidence = tile.getByLabelText("Minimum confidence for automatic sharing");
    await user.clear(priority);
    await user.clear(confidence);
    await user.click(tile.getByRole("switch", { name: "Get markers from this provider" }));
    await user.click(tile.getByRole("switch", { name: "Allow sharing with this provider" }));

    expect(priority).toBeEnabled();
    expect(confidence).toBeEnabled();
    expect(tile.getByRole("button", { name: "Save" })).toBeDisabled();
    await user.type(priority, "10");
    await user.type(confidence, "95");
    await user.click(tile.getByRole("button", { name: "Save" }));
    expect(mocks.updateMarkerProvider).toHaveBeenCalledWith(
      expect.objectContaining({
        patch: expect.objectContaining({
          fetch_enabled: false,
          fetch_priority: 10,
          contribute_enabled: false,
          contribute_auto_local: false,
          contribute_min_confidence: 0.95,
        }),
      }),
    );
  });

  it("disables sharing for providers that do not accept contributions", async () => {
    const user = userEvent.setup();
    markerProviders = [markerProvider({ is_submitter: false })];
    render(<ProvidersSettings />);
    const tile = within(screen.getByRole("group", { name: "TheIntroDB" }));
    await user.click(tile.getByRole("button", { name: "Manage" }));

    expect(tile.getByRole("switch", { name: "Allow sharing with this provider" })).toBeDisabled();
    expect(
      tile.getByRole("switch", { name: "Automatically share detected intros" }),
    ).toBeDisabled();
    expect(tile.getByLabelText("Minimum confidence for automatic sharing")).toBeDisabled();
    expect(tile.queryByRole("button", { name: "Test connection" })).not.toBeInTheDocument();
  });

  it("tests the marker provider connection and displays the result", async () => {
    const user = userEvent.setup();
    markerProviders = [markerProvider()];
    mocks.validateMarkerProvider.mockImplementation((_vars, options) => {
      options.onSuccess({ valid: true });
    });
    render(<ProvidersSettings />);
    const tile = within(screen.getByRole("group", { name: "TheIntroDB" }));
    await user.click(tile.getByRole("button", { name: "Manage" }));
    await user.click(tile.getByRole("button", { name: "Test connection" }));

    expect(mocks.validateMarkerProvider).toHaveBeenCalledWith(
      { provider: "plugin:6:introdb", displayName: "TheIntroDB" },
      expect.any(Object),
    );
    expect(tile.getByRole("status")).toHaveTextContent("Tested");
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
