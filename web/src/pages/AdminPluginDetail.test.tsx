import { act, fireEvent, render, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { PluginCatalogEntry, PluginInstallation } from "@/api/types";

import AdminPluginDetail from "./AdminPluginDetail";

type QueryState<T> = {
  data?: T;
  isLoading: boolean;
  isSuccess: boolean;
  isError: boolean;
  isFetching: boolean;
  refetch: () => void;
};

function query<T>(data: T | undefined, overrides: Partial<QueryState<T>> = {}): QueryState<T> {
  return {
    data,
    isLoading: false,
    isSuccess: data !== undefined,
    isError: false,
    isFetching: false,
    refetch: vi.fn(),
    ...overrides,
  };
}

let installationsQuery: QueryState<PluginInstallation[]> = query<PluginInstallation[]>([]);
let catalogQuery: QueryState<PluginCatalogEntry[]> = query<PluginCatalogEntry[]>([]);
const catalogOptions: Array<{ enabled?: boolean }> = [];
const updateInstallationMock = vi.fn();
let updateInstallationPending = false;
const applyUpdateMock = vi.fn();
const restartMock = vi.fn();
let restartPending = false;
const deleteMock = vi.fn();
const installMock = vi.fn();
const saveConfigMock = vi.fn();
const saveAuthBindingMock = vi.fn();
const saveTaskBindingMock = vi.fn();
const navigateToPluginRouteMock = vi.fn();
const capturedConfigForms: Array<Record<string, unknown>> = [];
const capturedSelects: Array<Record<string, unknown>> = [];
const capturedSwitches: Array<Record<string, unknown>> = [];

vi.mock("@/hooks/queries/admin/plugins", () => ({
  useAdminPluginInstallations: () => installationsQuery,
  useAdminPluginCatalog: (options: { enabled?: boolean }) => {
    catalogOptions.push(options);
    return catalogQuery;
  },
  useUpdatePluginInstallation: () => ({
    mutate: updateInstallationMock,
    isPending: updateInstallationPending,
  }),
  useApplyPluginUpdate: () => ({ mutate: applyUpdateMock, isPending: false }),
  useRestartPluginInstallation: () => ({ mutate: restartMock, isPending: restartPending }),
  useDeletePluginInstallation: () => ({ mutate: deleteMock, isPending: false }),
  useInstallPlugin: () => ({ mutate: installMock, isPending: false }),
  useSavePluginConfig: () => ({ mutate: saveConfigMock, isPending: false }),
  useTestPluginConfig: () => ({ mutateAsync: vi.fn(), isPending: false }),
  useSavePluginAuthBinding: () => ({ mutate: saveAuthBindingMock, isPending: false }),
  useSavePluginTaskBinding: () => ({ mutate: saveTaskBindingMock, isPending: false }),
}));

vi.mock("@/lib/buildPluginHref", () => ({
  navigateToPluginRoute: (href: string) => navigateToPluginRouteMock(href),
}));

vi.mock("@/components/admin/plugins/PluginConfigForm", () => ({
  PluginConfigForm: (props: Record<string, unknown>) => {
    capturedConfigForms.push(props);
    return <div data-testid="config-form">{(props.schema as { key: string }).key}</div>;
  },
}));

vi.mock("@/components/ui/select", () => ({
  Select: (props: Record<string, unknown>) => {
    capturedSelects.push(props);
    return null;
  },
  SelectContent: () => null,
  SelectItem: () => null,
  SelectTrigger: () => null,
  SelectValue: () => null,
}));

vi.mock("@/components/ui/switch", () => ({
  Switch: (props: Record<string, unknown>) => {
    capturedSwitches.push(props);
    return null;
  },
}));

vi.mock("@/components/ui/dropdown-menu", () => ({
  DropdownMenu: ({ children }: { children: ReactNode }) => <div>{children}</div>,
  DropdownMenuTrigger: ({ children }: { children: ReactNode }) => <div>{children}</div>,
  DropdownMenuContent: ({ children }: { children: ReactNode }) => <div role="menu">{children}</div>,
  DropdownMenuItem: ({
    children,
    onSelect,
    disabled,
  }: {
    children: ReactNode;
    onSelect?: () => void;
    disabled?: boolean;
  }) => (
    <button type="button" role="menuitem" disabled={disabled} onClick={() => onSelect?.()}>
      {children}
    </button>
  ),
  DropdownMenuSeparator: () => <hr />,
}));

vi.mock("@/components/ui/alert-dialog", () => ({
  AlertDialog: ({ open, children }: { open: boolean; children: ReactNode }) =>
    open ? <div role="alertdialog">{children}</div> : null,
  AlertDialogContent: ({ children }: { children: ReactNode }) => <div>{children}</div>,
  AlertDialogHeader: ({ children }: { children: ReactNode }) => <div>{children}</div>,
  AlertDialogFooter: ({ children }: { children: ReactNode }) => <div>{children}</div>,
  AlertDialogTitle: ({ children }: { children: ReactNode }) => <h2>{children}</h2>,
  AlertDialogDescription: ({ children }: { children: ReactNode }) => <p>{children}</p>,
  AlertDialogCancel: ({ children }: { children: ReactNode }) => (
    <button type="button">{children}</button>
  ),
  AlertDialogAction: ({ children, onClick }: { children: ReactNode; onClick?: () => void }) => (
    <button type="button" onClick={onClick}>
      {children}
    </button>
  ),
}));

const PRESENTATION = {
  display_name: "MDBList Ratings",
  summary: "Aggregated star ratings and age ratings from MDBList.",
  description_markdown: "Fills in ratings other providers leave empty.",
  setup_markdown: "Create a **free API key**, then add it below. <script>alert(1)</script>",
  homepage_url: "",
  source_url: "https://github.com/Silo-Server/silo-plugin-metadata-mdblist",
  support_url: "",
  changelog_url: "https://github.com/Silo-Server/silo-plugin-metadata-mdblist/releases",
  publisher_name: "Silo",
  publisher_url: "",
  license_spdx: "AGPL-3.0-only",
};

function makeInstallation(overrides: Partial<PluginInstallation> = {}): PluginInstallation {
  return {
    id: 7,
    repository_id: 1,
    plugin_id: "silo.mdblist",
    version: "0.1.0",
    install_path: "/plugins/mdblist",
    enabled: true,
    runtime: { resident: false, state: "stopped", restart_count: 0 },
    source_kind: "silo",
    repository_name: "Silo plugins",
    updates_paused: false,
    presentation: PRESENTATION,
    capabilities: [{ type: "metadata_provider.v1", id: "mdblist", display_name: "MDBList" }],
    global_config_schema: [
      {
        key: "account",
        title: "MDBList Account",
        description: "MDBList API key.",
        json_schema: "{}",
        required: true,
      },
    ],
    user_config_schema: [],
    routes: [],
    assets: [],
    global_configs: [],
    auth_bindings: [],
    task_bindings: [],
    update_policy: "auto",
    created_at: "2026-09-14T10:00:00Z",
    ...overrides,
  };
}

function makeCatalogEntry(overrides: Partial<PluginCatalogEntry> = {}): PluginCatalogEntry {
  return {
    repository_id: 1,
    plugin_id: "silo.manga-metadata",
    version: "0.1.1",
    archive_url: "https://example.com/manga",
    source_kind: "silo",
    repository_name: "Silo plugins",
    presentation: { ...PRESENTATION, display_name: "Manga Metadata", setup_markdown: "" },
    capabilities: [{ type: "metadata_provider.v1", id: "manga", display_name: "Manga" }],
    global_config_schema: [
      {
        key: "dump_path",
        title: "Dump storage path",
        description: "Where the dump is stored.",
        json_schema: "{}",
        required: false,
      },
    ],
    user_config_schema: [],
    routes: [],
    assets: [],
    ...overrides,
  };
}

function LocationProbe() {
  const location = useLocation();
  return <p data-testid="location">{`${location.pathname}${location.search}`}</p>;
}

function renderPage(pluginID = "silo.mdblist", search = "") {
  return render(
    <MemoryRouter initialEntries={[`/admin/plugins/${encodeURIComponent(pluginID)}${search}`]}>
      <Routes>
        <Route path="/admin/plugins/:pluginId" element={<AdminPluginDetail />} />
        <Route path="/admin/plugins" element={<LocationProbe />} />
      </Routes>
    </MemoryRouter>,
  );
}

describe("AdminPluginDetail", () => {
  beforeEach(() => {
    installationsQuery = query([makeInstallation()]);
    catalogQuery = query<PluginCatalogEntry[]>([]);
    catalogOptions.length = 0;
    capturedConfigForms.length = 0;
    capturedSelects.length = 0;
    capturedSwitches.length = 0;
    restartPending = false;
    updateInstallationPending = false;
    for (const mock of [
      updateInstallationMock,
      applyUpdateMock,
      restartMock,
      deleteMock,
      installMock,
      saveConfigMock,
      saveAuthBindingMock,
      saveTaskBindingMock,
      navigateToPluginRouteMock,
    ])
      mock.mockReset();
  });

  it("shows the plugin's header, details, and one settings panel per schema entry", () => {
    renderPage();

    expect(screen.getByRole("heading", { level: 1, name: "MDBList Ratings" })).toBeInTheDocument();
    expect(screen.getByText("Version 0.1.0")).toBeInTheDocument();
    expect(screen.getAllByText("Silo maintained").length).toBeGreaterThan(0);
    expect(screen.getByText("silo.mdblist")).toBeInTheDocument();
    expect(screen.getByText("AGPL-3.0-only")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "MDBList Account" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /Changelog/ })).toHaveAttribute(
      "href",
      "https://github.com/Silo-Server/silo-plugin-metadata-mdblist/releases",
    );
    expect(capturedConfigForms).toHaveLength(1);
    expect(capturedConfigForms[0]?.bare).toBe(true);
    // An installed plugin with presentation never waits on the live catalog fetch.
    expect(catalogOptions.every((options) => options.enabled === false)).toBe(true);
  });

  it("saves a settings panel against the installation", () => {
    renderPage();
    const onSave = capturedConfigForms[0]?.onSave as (
      key: string,
      value: Record<string, unknown>,
      clear: string[],
    ) => void;
    onSave("account", { api_key: "k" }, []);
    expect(saveConfigMock).toHaveBeenCalledWith({
      id: 7,
      body: { key: "account", value: { api_key: "k" }, clear_secrets: [] },
    });
  });

  it("asks for missing required settings, but not for optional ones", () => {
    const { unmount } = renderPage();
    expect(screen.getByText("Finish setting up MDBList Ratings.")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "MDBList Account" })).toHaveAttribute(
      "href",
      "#config-account",
    );
    expect(screen.getAllByText("Needs setup").length).toBeGreaterThan(0);
    unmount();

    installationsQuery = query([
      makeInstallation({
        global_config_schema: [
          { key: "sources", title: "Search sources", json_schema: "{}", required: false },
        ],
      }),
    ]);
    renderPage();
    expect(screen.queryByText(/Finish setting up/)).not.toBeInTheDocument();
    expect(screen.queryByText("Needs setup")).not.toBeInTheDocument();
  });

  it("leads with the runtime error when a resident plugin keeps failing", () => {
    installationsQuery = query([
      makeInstallation({
        global_config_schema: [],
        runtime: {
          resident: true,
          state: "backoff",
          restart_count: 3,
          last_error: "floppy: context deadline exceeded",
        },
      }),
    ]);
    renderPage();

    expect(screen.getByText("MDBList Ratings keeps stopping.")).toBeInTheDocument();
    expect(screen.getByText("floppy: context deadline exceeded")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Restart now" }));
    expect(restartMock).toHaveBeenCalledWith(7);
  });

  it.each([
    { enabled: false, resident: true, state: "running", visible: false, disabled: false },
    { enabled: true, resident: false, state: "stopped", visible: false, disabled: false },
    { enabled: true, resident: true, state: "starting", visible: true, disabled: true },
    { enabled: true, resident: true, state: "running", visible: true, disabled: false },
  ] as const)("only offers Restart to enabled resident plugins: %j", (testCase) => {
    installationsQuery = query([
      makeInstallation({
        enabled: testCase.enabled,
        runtime: { resident: testCase.resident, state: testCase.state, restart_count: 0 },
      }),
    ]);
    renderPage();
    const restart = screen.queryByRole("menuitem", { name: "Restart" });
    expect(Boolean(restart)).toBe(testCase.visible);
    if (restart) expect(restart).toHaveProperty("disabled", testCase.disabled);
  });

  it("changes the update policy, the enabled state, and applies an update", () => {
    installationsQuery = query([makeInstallation({ available_version: "0.2.0" })]);
    renderPage();

    (capturedSelects[0]?.onValueChange as (value: string) => void)("notify");
    expect(updateInstallationMock).toHaveBeenCalledWith({
      id: 7,
      body: { update_policy: "notify" },
    });
    const enabled = capturedSwitches.find((props) => props["aria-label"] === "Enabled");
    (enabled?.onCheckedChange as (checked: boolean) => void)(false);
    expect(updateInstallationMock).toHaveBeenCalledWith({ id: 7, body: { enabled: false } });

    fireEvent.click(screen.getByRole("menuitem", { name: "Update to 0.2.0" }));
    expect(applyUpdateMock).toHaveBeenCalledWith(7);
  });

  it("locks the update policy while an installation update is saving", () => {
    updateInstallationPending = true;
    renderPage();
    expect(capturedSelects[0]?.disabled).toBe(true);
  });

  it("uninstalls after confirmation and returns to the plugin list", () => {
    renderPage();
    fireEvent.click(screen.getByRole("menuitem", { name: "Uninstall..." }));
    fireEvent.click(screen.getByRole("button", { name: "Uninstall plugin" }));

    expect(deleteMock).toHaveBeenCalledWith(7, { onSuccess: expect.any(Function) });
    const options = deleteMock.mock.calls[0]?.[1] as { onSuccess: () => void };
    act(() => options.onSuccess());
    expect(screen.getByTestId("location")).toHaveTextContent("/admin/plugins");
  });

  it("opens a plugin's own admin pages", () => {
    installationsQuery = query([
      makeInstallation({
        routes: [
          {
            id: "dashboard",
            method: "GET",
            path: "/dashboard/*",
            access: "admin",
            navigable: true,
            navigation_label: "Dashboard",
            navigation_kind: "admin",
            static_asset: true,
          },
        ],
      }),
    ]);
    renderPage();
    fireEvent.click(screen.getByRole("menuitem", { name: "Dashboard" }));
    expect(navigateToPluginRouteMock).toHaveBeenCalledWith(
      "/api/v2/plugin-content/plugins/7/dashboard",
    );
  });

  it("describes a scheduled task's default schedule instead of showing raw JSON", () => {
    installationsQuery = query([
      makeInstallation({
        global_config_schema: [],
        capabilities: [{ type: "scheduled_task.v1", id: "refresh", display_name: "Refresh lists" }],
        task_bindings: [
          {
            capability_id: "refresh",
            enabled: true,
            trigger: { type: "daily", time_of_day: "03:00" },
            created_at: "",
            updated_at: "",
          },
        ],
      }),
    ]);
    renderPage();

    expect(screen.getByText("Default schedule: Daily at 03:00")).toBeInTheDocument();
    expect(screen.queryByText(/\{"type"/)).not.toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Schedule and history" })).toHaveAttribute(
      "href",
      "/admin/tasks/plugin%3A7%3Arefresh",
    );
    const run = capturedSwitches.find((props) => props["aria-label"] === "Run Refresh lists");
    (run?.onCheckedChange as (checked: boolean) => void)(false);
    expect(saveTaskBindingMock).toHaveBeenCalledWith({
      id: 7,
      capabilityId: "refresh",
      body: { enabled: false, trigger: { type: "daily", time_of_day: "03:00" } },
    });
  });

  it("saves a sign-in binding with its existing order", () => {
    installationsQuery = query([
      makeInstallation({
        global_config_schema: [],
        capabilities: [{ type: "auth_provider.v1", id: "oidc", display_name: "Company SSO" }],
      }),
    ]);
    renderPage();
    const toggle = capturedSwitches.find(
      (props) => props["aria-label"] === "Offer Company SSO on the sign-in screen",
    );
    (toggle?.onCheckedChange as (checked: boolean) => void)(true);
    expect(saveAuthBindingMock).toHaveBeenCalledWith({
      id: 7,
      body: {
        capability_id: "oidc",
        enabled: true,
        display_order: 1,
        auto_provision: true,
        default_login: false,
      },
    });
  });

  it("says so when a plugin has no settings", () => {
    installationsQuery = query([makeInstallation({ global_config_schema: [] })]);
    renderPage();
    expect(screen.getByText(/This plugin has no settings/)).toBeInTheDocument();
    expect(capturedConfigForms).toHaveLength(0);
  });

  it.each([
    ["approved_community", "Approved community."],
    ["external", "External source."],
  ] as const)("shows the %s tier notice", (sourceKind, heading) => {
    installationsQuery = query([makeInstallation({ source_kind: sourceKind })]);
    renderPage();
    expect(screen.getByText(heading)).toBeInTheDocument();
  });

  it("shows no tier notice for Silo's own plugins", () => {
    renderPage();
    expect(screen.queryByRole("note")).not.toBeInTheDocument();
  });

  it("renders setup Markdown as text, never as HTML", () => {
    const { container } = renderPage();
    expect(screen.getByText("free API key").tagName).toBe("STRONG");
    expect(container.querySelector("script")).toBeNull();
    expect(screen.getByText(/<script>alert\(1\)<\/script>/)).toBeInTheDocument();
  });

  it("falls back to catalog presentation for an older installed manifest", () => {
    installationsQuery = query([makeInstallation({ presentation: undefined })]);
    catalogQuery = query([
      makeCatalogEntry({
        plugin_id: "silo.mdblist",
        presentation: { ...PRESENTATION, display_name: "MDBList From Catalog" },
      }),
    ]);
    renderPage();
    expect(catalogOptions.at(-1)?.enabled).toBe(true);
    expect(
      screen.getByRole("heading", { level: 1, name: "MDBList From Catalog" }),
    ).toBeInTheDocument();
  });

  it("previews a catalog plugin that isn't installed and installs it", () => {
    installationsQuery = query<PluginInstallation[]>([]);
    catalogQuery = query([makeCatalogEntry()]);
    renderPage("silo.manga-metadata");

    expect(screen.getByRole("heading", { level: 1, name: "Manga Metadata" })).toBeInTheDocument();
    expect(screen.getByText("Not installed")).toBeInTheDocument();
    expect(screen.getByText("Dump storage path")).toBeInTheDocument();
    expect(capturedSwitches).toHaveLength(0);
    fireEvent.click(screen.getByRole("button", { name: "Install" }));
    expect(installMock).toHaveBeenCalledWith({
      repository_id: 1,
      plugin_id: "silo.manga-metadata",
      version: "0.1.1",
    });
  });

  it("installs the catalog version selected from its tile", () => {
    installationsQuery = query<PluginInstallation[]>([]);
    catalogQuery = query([
      makeCatalogEntry({ version: "0.1.0" }),
      makeCatalogEntry({ version: "0.2.0" }),
    ]);
    renderPage("silo.manga-metadata", "?repository=1&version=0.2.0");
    expect(screen.getByText("Version 0.2.0")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Install" }));
    expect(installMock).toHaveBeenCalledWith({
      repository_id: 1,
      plugin_id: "silo.manga-metadata",
      version: "0.2.0",
    });
  });

  it("shows loading, then not found, for an unknown plugin", () => {
    installationsQuery = query<PluginInstallation[]>(undefined, { isLoading: true });
    const { unmount } = renderPage("silo.unknown");
    expect(screen.getByText("Loading plugin...")).toBeInTheDocument();
    expect(screen.queryByText("Plugin not found")).not.toBeInTheDocument();
    unmount();

    installationsQuery = query<PluginInstallation[]>([]);
    catalogQuery = query<PluginCatalogEntry[]>([]);
    renderPage("silo.unknown");
    expect(screen.getByText("Plugin not found")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "All plugins" })).toHaveAttribute(
      "href",
      "/admin/plugins",
    );
  });

  it("offers a retry when the installations can't be read", () => {
    const refetch = vi.fn();
    installationsQuery = query<PluginInstallation[]>(undefined, { isError: true, refetch });
    renderPage();
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(refetch).toHaveBeenCalled();
  });
});
