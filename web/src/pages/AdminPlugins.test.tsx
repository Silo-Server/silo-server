import { render, screen } from "@testing-library/react";
import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter, Route, Routes, useLocation, useNavigationType } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { PluginCatalogEntry, PluginInstallation } from "@/api/types";

import AdminPlugins from "./AdminPlugins";

const useAdminPluginsMock = vi.fn();
const checkPluginUpdatesMutateMock = vi.fn();
const updatePluginCatalogSettingsMutateMock = vi.fn();
const installPluginMutateMock = vi.fn();
const capturedButtonProps: Array<Record<string, unknown>> = [];
const capturedSwitchProps: Array<Record<string, unknown>> = [];

function makeCatalogEntry(
  index: number,
  overrides: { displayName?: string; summary?: string } = {},
): PluginCatalogEntry {
  const suffix = String(index).padStart(2, "0");
  const repoURL = `https://github.com/Silo-Server/plugin-${suffix}`;
  return {
    repository_id: 1,
    plugin_id: `silo.plugin-${suffix}`,
    version: "1.0.0",
    archive_url: `${repoURL}/releases/download/v1.0.0/plugin-linux-amd64`,
    source_kind: "silo",
    repository_name: "Silo plugins",
    repo_url: repoURL,
    presentation: {
      display_name: overrides.displayName ?? `Plugin ${suffix}`,
      summary: overrides.summary ?? `Summary for plugin ${suffix}.`,
      description_markdown: `Description for plugin ${suffix}.`,
      setup_markdown: "Install and configure it.",
      homepage_url: repoURL,
      source_url: repoURL,
      support_url: `${repoURL}/issues`,
      changelog_url: `${repoURL}/releases`,
      publisher_name: "Silo",
      publisher_url: "https://github.com/Silo-Server",
      license_spdx: "AGPL-3.0-or-later",
    },
    capabilities: [],
    global_config_schema: [],
    user_config_schema: [],
    routes: [],
    assets: [],
  };
}

function makeInstallation(index: number, displayName: string): PluginInstallation {
  const suffix = String(index).padStart(2, "0");
  return {
    id: index,
    repository_id: 1,
    plugin_id: `silo.installed-${suffix}`,
    version: "1.0.0",
    install_path: `/plugins/installed-${suffix}`,
    enabled: true,
    runtime: { resident: false, state: "stopped", restart_count: 0 },
    source_kind: "silo",
    repository_name: "Silo plugins",
    updates_paused: false,
    presentation: {
      display_name: displayName,
      summary: `Installed summary ${suffix}.`,
      description_markdown: `Installed description ${suffix}.`,
      setup_markdown: "Configure it.",
      homepage_url: "",
      source_url: `https://github.com/Silo-Server/installed-${suffix}`,
      support_url: "",
      changelog_url: "",
      publisher_name: "Silo",
      publisher_url: "https://github.com/Silo-Server",
      license_spdx: "AGPL-3.0-or-later",
    },
    capabilities: [],
    global_config_schema: [],
    user_config_schema: [],
    routes: [],
    assets: [],
    global_configs: [],
    auth_bindings: [],
    task_bindings: [],
    update_policy: "auto",
  };
}

vi.mock("@/components/ui/button", () => ({
  Button: (props: Record<string, unknown>) => {
    capturedButtonProps.push(props);
    return props.children;
  },
}));

vi.mock("@/components/ui/tabs", () => ({
  Tabs: (props: Record<string, unknown>) => props.children,
  TabsList: (props: Record<string, unknown>) => props.children,
  TabsTrigger: (props: Record<string, unknown>) => props.children,
  TabsContent: (props: Record<string, unknown>) => props.children,
}));

vi.mock("@/components/ui/switch", () => ({
  Switch: (props: Record<string, unknown>) => {
    capturedSwitchProps.push(props);
    return null;
  },
}));

vi.mock("@tanstack/react-query", async () => {
  const actual =
    await vi.importActual<typeof import("@tanstack/react-query")>("@tanstack/react-query");
  return {
    ...actual,
    useQueryClient: () => ({ invalidateQueries: vi.fn() }),
  };
});

vi.mock("@/hooks/queries/admin/plugins", () => ({
  CHECK_PLUGIN_UPDATES_TASK_KEY: "check_plugin_updates",
  useAdminPlugins: () => useAdminPluginsMock(),
  useCheckPluginUpdates: () => ({ mutate: checkPluginUpdatesMutateMock, isPending: false }),
  useUpdatePluginCatalogSettings: () => ({
    mutate: updatePluginCatalogSettingsMutateMock,
    isPending: false,
  }),
  useCreatePluginRepository: () => ({ mutate: vi.fn(), isPending: false }),
  useUpdatePluginRepository: () => ({ mutate: vi.fn(), isPending: false }),
  useDeletePluginRepository: () => ({ mutate: vi.fn(), isPending: false }),
  useInstallPlugin: () => ({ mutate: installPluginMutateMock, isPending: false }),
  useUploadPlugin: () => ({ mutate: vi.fn(), isPending: false }),
  usePluginUpload: () => ({ upload: vi.fn(), progress: null, isPending: false }),
  useUpdatePluginInstallation: () => ({ mutate: vi.fn(), isPending: false }),
  useApplyPluginUpdate: () => ({ mutate: vi.fn(), isPending: false }),
  useRestartPluginInstallation: () => ({ mutate: vi.fn(), isPending: false }),
  useDeletePluginInstallation: () => ({ mutate: vi.fn(), isPending: false }),
  useSavePluginConfig: () => ({ mutate: vi.fn(), isPending: false }),
  useTestPluginConfig: () => ({ mutate: vi.fn(), isPending: false }),
  useSavePluginAuthBinding: () => ({ mutate: vi.fn(), isPending: false }),
  useSavePluginTaskBinding: () => ({ mutate: vi.fn(), isPending: false }),
}));

vi.mock("@/hooks/queries/admin/tasks", () => ({
  useTask: () => ({ data: { key: "check_plugin_updates", state: "idle" } }),
}));

describe("AdminPlugins", () => {
  beforeEach(() => {
    capturedButtonProps.length = 0;
    capturedSwitchProps.length = 0;
    checkPluginUpdatesMutateMock.mockReset();
    updatePluginCatalogSettingsMutateMock.mockReset();
    installPluginMutateMock.mockReset();
    useAdminPluginsMock.mockReturnValue({
      repositories: [],
      catalog: [],
      installations: [],
      catalogSettings: undefined,
      isLoading: false,
    });
  });

  it("starts the shared plugin update check task from the plugins page", () => {
    renderToStaticMarkup(
      <MemoryRouter>
        <AdminPlugins />
      </MemoryRouter>,
    );

    const button = capturedButtonProps.find((props) => {
      const children = props.children;
      if (typeof children === "string") {
        return children === "Check for updates";
      }
      return Array.isArray(children) && children.some((child) => child === "Check for updates");
    });

    expect(button).toBeTruthy();
    expect(typeof button?.onClick).toBe("function");

    (button?.onClick as () => void)();

    expect(checkPluginUpdatesMutateMock).toHaveBeenCalledTimes(1);
  });

  it("enables the approved community catalog directly when no community plugins are installed", () => {
    useAdminPluginsMock.mockReturnValue({
      repositories: [],
      catalog: [],
      installations: [],
      catalogSettings: {
        etag: '"catalog-snapshot"',
        include_approved_community_plugins: false,
        approved_community_plugin_count: 2,
        installed_community_plugin_count: 0,
        migrated_plugin_count: 0,
        community_updates_paused: false,
      },
      isLoading: false,
    });

    renderToStaticMarkup(
      <MemoryRouter>
        <AdminPlugins />
      </MemoryRouter>,
    );

    const onCheckedChange = capturedSwitchProps[0]?.onCheckedChange as (checked: boolean) => void;
    onCheckedChange(true);

    expect(updatePluginCatalogSettingsMutateMock).toHaveBeenCalledWith({
      etag: '"catalog-snapshot"',
      include_approved_community_plugins: true,
    });
  });

  it("does not disable the community catalog without confirmation when plugins are installed", () => {
    useAdminPluginsMock.mockReturnValue({
      repositories: [],
      catalog: [],
      installations: [],
      catalogSettings: {
        etag: '"catalog-snapshot"',
        include_approved_community_plugins: true,
        approved_community_plugin_count: 2,
        installed_community_plugin_count: 2,
        migrated_plugin_count: 2,
        community_updates_paused: false,
      },
      isLoading: false,
    });

    renderToStaticMarkup(
      <MemoryRouter>
        <AdminPlugins />
      </MemoryRouter>,
    );

    const onCheckedChange = capturedSwitchProps[0]?.onCheckedChange as (checked: boolean) => void;
    onCheckedChange(false);

    expect(updatePluginCatalogSettingsMutateMock).not.toHaveBeenCalled();
  });

  it("searches catalog presentation metadata from the URL", () => {
    useAdminPluginsMock.mockReturnValue({
      repositories: [],
      installations: [],
      catalogSettings: undefined,
      isLoading: false,
      catalog: [
        makeCatalogEntry(1, {
          displayName: "Alpha Scanner",
          summary: "Indexes local media files.",
        }),
        makeCatalogEntry(2, {
          displayName: "Needle Requests",
          summary: "Routes requests to the right service.",
        }),
      ],
    });

    const markup = renderToStaticMarkup(
      <MemoryRouter initialEntries={["/admin/plugins?tab=catalog&catalog_q=needle"]}>
        <AdminPlugins />
      </MemoryRouter>,
    );

    expect(markup).toContain("Needle Requests");
    expect(markup).toContain("1 of 2 plugins");
    expect(markup).not.toContain("Alpha Scanner");
  });

  it("searches installed plugin metadata independently from the catalog", () => {
    useAdminPluginsMock.mockReturnValue({
      repositories: [],
      catalog: [],
      catalogSettings: undefined,
      isLoading: false,
      installations: [
        makeInstallation(1, "Alpha Metadata"),
        makeInstallation(2, "Needle Automation"),
      ],
    });

    const markup = renderToStaticMarkup(
      <MemoryRouter initialEntries={["/admin/plugins?installed_q=needle"]}>
        <AdminPlugins />
      </MemoryRouter>,
    );

    expect(markup).toContain("Needle Automation");
    expect(markup).toContain("1 of 2 plugins");
    expect(markup).not.toContain("Alpha Metadata");
  });

  it("finds a plugin installed without a catalog under the Unverified label it shows", () => {
    useAdminPluginsMock.mockReturnValue({
      repositories: [],
      catalog: [],
      catalogSettings: undefined,
      isLoading: false,
      installations: [
        makeInstallation(1, "Alpha Metadata"),
        {
          ...makeInstallation(2, "Uploaded Metadata"),
          repository_id: null,
          source_kind: "external",
        },
      ],
    });

    const markup = renderToStaticMarkup(
      <MemoryRouter initialEntries={["/admin/plugins?installed_q=unverified"]}>
        <AdminPlugins />
      </MemoryRouter>,
    );

    expect(markup).toContain("Uploaded Metadata");
    expect(markup).not.toContain("Alpha Metadata");
  });

  it("groups the catalog by source and installs from a tile", () => {
    const external: PluginCatalogEntry = {
      ...makeCatalogEntry(3, { displayName: "Home Lab Tool" }),
      plugin_id: "lab.tool",
      source_kind: "external",
      repository_id: 3,
    };
    useAdminPluginsMock.mockReturnValue({
      repositories: [],
      catalogSettings: undefined,
      isLoading: false,
      installations: [{ ...makeInstallation(1, "Installed One"), plugin_id: "silo.plugin-01" }],
      catalog: [makeCatalogEntry(1), makeCatalogEntry(2), external],
    });

    const markup = renderToStaticMarkup(
      <MemoryRouter initialEntries={["/admin/plugins?tab=catalog"]}>
        <AdminPlugins />
      </MemoryRouter>,
    );

    expect(markup).toContain("Made by Silo");
    expect(markup).toContain("Other sources");
    expect(markup).not.toContain("Approved community");
    // Uninstalled plugins come first within a group.
    expect(markup.indexOf("Plugin 02")).toBeLessThan(markup.indexOf("Plugin 01"));
    expect(markup).toContain("Installed");

    const install = capturedButtonProps.find(
      (props) => props["aria-label"] === "Install Plugin 02",
    );
    (install?.onClick as () => void)();
    expect(installPluginMutateMock).toHaveBeenCalledWith({
      repository_id: 1,
      plugin_id: "silo.plugin-02",
      version: "1.0.0",
    });
  });

  it("filters the catalog by job from the URL", () => {
    useAdminPluginsMock.mockReturnValue({
      repositories: [],
      installations: [],
      catalogSettings: undefined,
      isLoading: false,
      catalog: [
        {
          ...makeCatalogEntry(1, { displayName: "Intro Finder" }),
          capabilities: [{ type: "marker_provider.v1", id: "intro", display_name: "Intro" }],
        },
        {
          ...makeCatalogEntry(2, { displayName: "Ratings Source" }),
          capabilities: [{ type: "metadata_provider.v1", id: "ratings", display_name: "Ratings" }],
        },
      ],
    });

    const markup = renderToStaticMarkup(
      <MemoryRouter initialEntries={["/admin/plugins?tab=catalog&catalog_job=markers"]}>
        <AdminPlugins />
      </MemoryRouter>,
    );

    expect(markup).toContain("Intro Finder");
    expect(markup).not.toContain("Ratings Source");
    expect(markup).toContain("1 of 2 plugins");
  });

  it("keeps a job filter when the search leaves nothing for that job", () => {
    useAdminPluginsMock.mockReturnValue({
      repositories: [],
      installations: [],
      catalogSettings: undefined,
      isLoading: false,
      catalog: [
        {
          ...makeCatalogEntry(1, { displayName: "Intro Finder" }),
          capabilities: [{ type: "marker_provider.v1", id: "intro", display_name: "Intro" }],
        },
        {
          ...makeCatalogEntry(2, { displayName: "Ratings Source" }),
          capabilities: [{ type: "metadata_provider.v1", id: "ratings", display_name: "Ratings" }],
        },
      ],
    });

    const markup = renderToStaticMarkup(
      <MemoryRouter
        initialEntries={["/admin/plugins?tab=catalog&catalog_job=markers&catalog_q=ratings"]}
      >
        <AdminPlugins />
      </MemoryRouter>,
    );

    expect(markup).not.toContain("Ratings Source");
    expect(markup).toContain("No catalog plugins match");
  });

  it.each([["silo.theintrodb"], ["silo.not-installed"]])(
    "forwards the old ?configure=%s link to the plugin page",
    async (pluginID) => {
      function Probe() {
        const location = useLocation();
        const navigationType = useNavigationType();
        return <p>{`${location.pathname} ${navigationType}`}</p>;
      }
      useAdminPluginsMock.mockReturnValue({
        repositories: [],
        catalog: [],
        installations: [makeInstallation(1, "TheIntroDB")],
        catalogSettings: undefined,
        isLoading: false,
      });

      render(
        <MemoryRouter
          initialEntries={[`/admin/plugins?installed_q=${pluginID}&configure=${pluginID}`]}
        >
          <Routes>
            <Route path="/admin/plugins" element={<AdminPlugins />} />
            <Route path="/admin/plugins/:pluginId" element={<Probe />} />
          </Routes>
        </MemoryRouter>,
      );

      expect(await screen.findByText(`/admin/plugins/${pluginID} REPLACE`)).toBeInTheDocument();
    },
  );
});
