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
let pluginInstallations: Partial<PluginInstallation>[] | undefined = [];
let installationsError = false;
const refetchInstallations = vi.fn();

vi.mock("@/hooks/queries/admin/markers", () => ({
  useMarkerProviders: () => ({ data: { providers: markerProviders }, isLoading: false }),
  useUpdateMarkerProvider: () => ({ mutate: mocks.updateMarkerProvider, isPending: false }),
  useValidateMarkerProvider: () => ({ mutate: mocks.validateMarkerProvider, isPending: false }),
}));

vi.mock("@/hooks/queries/admin/plugins", () => ({
  useAdminPluginInstallations: () => ({
    data: pluginInstallations,
    isLoading: false,
    isError: installationsError,
    refetch: refetchInstallations,
  }),
}));

let sensitiveConfigured: string[] = ["mdblist.api_key"];
let settingsValues: Record<string, string> = {};
const setValueMock = vi.fn();

const useSettingsFormMock = vi.fn((_options?: { keys: string[] }) => ({
  isLoading: false,
  getValue: (key: string) => settingsValues[key] ?? "",
  setValue: setValueMock,
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
    setValueMock.mockReset();
    markerProviders = [];
    pluginInstallations = [];
    installationsError = false;
    refetchInstallations.mockReset();
    for (const mock of Object.values(mocks)) mock.mockReset();
  });

  it("leaves subtitle sync to Playback and stages nothing through a save bar", () => {
    render(<ProvidersSettings />);

    expect(screen.getByRole("heading", { level: 1, name: "Providers" })).toBeInTheDocument();
    expect(useSettingsFormMock).toHaveBeenLastCalledWith({ keys: ["mdblist.api_key"] });
    expect(screen.queryByRole("group", { name: "Subtitle sync" })).not.toBeInTheDocument();
    expect(screen.queryByText("Sync subtitles automatically")).not.toBeInTheDocument();
    const groups = ["Watch providers", "Lists", "Subtitle providers", "Marker providers"];
    const headings = Array.from(document.querySelectorAll("h3"), (h) => h.textContent ?? "");
    expect(headings.filter((heading) => groups.includes(heading))).toEqual(groups);
    expect(setValueMock).not.toHaveBeenCalled();
  });

  it("files the MDBList key under Lists and points metadata providers to Plugins", () => {
    render(<ProvidersSettings />);

    const lists = screen.getByRole("group", { name: "Lists" });
    const mdblist = within(lists).getByRole("group", { name: "MDBList" });
    expect(within(mdblist).getByText("Lists")).toBeInTheDocument();
    expect(screen.queryByRole("group", { name: "Metadata providers" })).not.toBeInTheDocument();
    expect(screen.queryByText("Ratings and lists")).not.toBeInTheDocument();
    expect(
      screen.getByText(/Metadata providers, including TMDB, TheTVDB, and MDBList ratings/),
    ).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Plugins" })).toHaveAttribute("href", "/admin/plugins");
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
    expect(mocks.updateProvider).toHaveBeenCalledWith(
      {
        editor: expect.objectContaining({
          etag: '"captured"',
          intent: expect.objectContaining({ provider: "subdl" }),
        }),
        // SubDL had no key, so its first one switches it on.
        config: { enabled: true, api_key: "retained-secret" },
      },
      expect.anything(),
    );
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

  it("switches a subtitle provider on with its first credentials", async () => {
    const user = userEvent.setup();
    render(<ProvidersSettings />);
    await user.click(
      within(screen.getByRole("group", { name: "SubDL" })).getByRole("button", { name: "Connect" }),
    );
    const panel = within(screen.getByRole("group", { name: "SubDL" }));
    await waitFor(() => expect(panel.getByRole("switch", { name: "Enable SubDL" })).toBeEnabled());
    expect(panel.getByRole("switch", { name: "Enable SubDL" })).not.toBeChecked();

    await user.type(panel.getByLabelText("API key"), "first-key");
    // The switch shows what Save will do before it does it.
    expect(panel.getByRole("switch", { name: "Enable SubDL" })).toBeChecked();
    await user.click(panel.getByRole("button", { name: "Save" }));

    expect(mocks.updateProvider).toHaveBeenCalledWith(
      expect.objectContaining({ config: { enabled: true, api_key: "first-key" } }),
      expect.anything(),
    );
  });

  it("keeps a first-connected provider off when the admin switched it off", async () => {
    const user = userEvent.setup();
    render(<ProvidersSettings />);
    await user.click(
      within(screen.getByRole("group", { name: "SubDL" })).getByRole("button", { name: "Connect" }),
    );
    const panel = within(screen.getByRole("group", { name: "SubDL" }));
    await waitFor(() => expect(panel.getByRole("switch", { name: "Enable SubDL" })).toBeEnabled());

    await user.type(panel.getByLabelText("API key"), "first-key");
    await user.click(panel.getByRole("switch", { name: "Enable SubDL" }));
    await user.click(panel.getByRole("button", { name: "Save" }));

    expect(mocks.updateProvider).toHaveBeenCalledWith(
      expect.objectContaining({ config: { enabled: false, api_key: "first-key" } }),
      expect.anything(),
    );
  });

  it("leaves a configured provider that is off alone when its key is replaced", async () => {
    const user = userEvent.setup();
    render(<ProvidersSettings />);
    await user.click(
      within(screen.getByRole("group", { name: "SubSource" })).getByRole("button", {
        name: "Manage",
      }),
    );
    const panel = within(screen.getByRole("group", { name: "SubSource" }));
    await waitFor(() =>
      expect(panel.getByRole("switch", { name: "Enable SubSource" })).toBeEnabled(),
    );

    await user.type(panel.getByLabelText("API key"), "rotated-key");
    expect(panel.getByRole("switch", { name: "Enable SubSource" })).not.toBeChecked();
    await user.click(panel.getByRole("button", { name: "Save" }));

    expect(mocks.updateProvider).toHaveBeenCalledWith(
      expect.objectContaining({ config: { enabled: false, api_key: "rotated-key" } }),
      expect.anything(),
    );
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
    // Lookup order and the sharing threshold have working defaults, so they
    // wait under the tile's Advanced section.
    expect(within(tile).queryByLabelText("Provider priority")).not.toBeInTheDocument();
    expect(
      within(tile).queryByLabelText("Minimum confidence for automatic sharing"),
    ).not.toBeInTheDocument();
    expect(
      within(tile).getByRole("switch", { name: "Get markers from this provider" }),
    ).toBeChecked();
    await user.click(within(tile).getByRole("button", { name: "Advanced · 2 settings" }));
    expect(within(tile).getByLabelText("Provider priority")).toHaveValue(10);
    expect(within(tile).getByLabelText("Minimum confidence for automatic sharing")).toHaveValue(95);
    // Credentials are the plugin's, not Silo's: the panel links out for them.
    expect(within(tile).getByRole("link", { name: "plugin page" })).toHaveAttribute(
      "href",
      "/admin/plugins/silo.theintrodb",
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

  it.each([0, 92.5, 100])(
    "saves a confidence of %s percent in the API's 0–1 range",
    async (percent) => {
      const user = userEvent.setup();
      markerProviders = [markerProvider({ contribute_enabled: true, contribute_auto_local: true })];
      render(<ProvidersSettings />);
      const tile = within(screen.getByRole("group", { name: "TheIntroDB" }));
      await user.click(tile.getByRole("button", { name: "Manage" }));
      await user.click(tile.getByRole("button", { name: /^Advanced/ }));

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
      await user.click(tile.getByRole("button", { name: /^Advanced/ }));

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
    await user.click(tile.getByRole("button", { name: /^Advanced/ }));

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
    await user.click(tile.getByRole("button", { name: /^Advanced/ }));

    const sharing = tile.getByRole("switch", { name: "Allow sharing with this provider" });
    const automatic = tile.getByRole("switch", {
      name: "Automatically share detected intros and credits",
    });
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
    await user.click(tile.getByRole("button", { name: /^Advanced/ }));
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
    await user.click(tile.getByRole("button", { name: /^Advanced/ }));

    expect(tile.getByRole("switch", { name: "Allow sharing with this provider" })).toBeDisabled();
    expect(
      tile.getByRole("switch", { name: "Automatically share detected intros and credits" }),
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

  it("reports a failed watch provider load instead of claiming nothing is installed", async () => {
    pluginInstallations = undefined;
    installationsError = true;
    render(<ProvidersSettings />);

    const watch = screen.getByRole("group", { name: "Watch providers" });
    expect(within(watch).getByRole("alert")).toHaveTextContent(
      "Couldn't load the installed watch provider plugins.",
    );
    expect(screen.queryByText("No watch provider plugins are installed.")).not.toBeInTheDocument();
    await userEvent.setup().click(within(watch).getByRole("button", { name: "Retry" }));
    expect(refetchInstallations).toHaveBeenCalled();
  });

  it("does not label an enabled watch plugin as connected until its required config is set", () => {
    const schema = [{ key: "account", title: "Account", json_schema: "{}", required: true }];
    pluginInstallations = [
      {
        id: 7,
        plugin_id: "silo-plugin-watchsync-anilist",
        enabled: true,
        capabilities: [{ type: "watch_sync_provider.v1", id: "anilist", display_name: "AniList" }],
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

    render(<ProvidersSettings />);

    // Enabled but keyless: the plugin cannot serve a request, so the tile must
    // not read as set up.
    const watch = screen.getByRole("group", { name: "Watch providers" });
    const anilist = within(watch).getByRole("group", { name: "AniList" });
    expect(anilist).toHaveAttribute("data-state", "not_connected");
    expect(within(anilist).getByText("Needs setup")).toBeInTheDocument();

    const serializd = within(watch).getByRole("group", { name: "Serializd" });
    expect(serializd).toHaveAttribute("data-state", "connected");
    expect(within(serializd).getByText("Enabled")).toBeInTheDocument();
  });
});
