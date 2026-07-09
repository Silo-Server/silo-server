import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { PluginCatalogEntry, PluginInstallation } from "@/api/types";

import AdminPlugins from "./AdminPlugins";

const useAdminPluginsMock = vi.fn();
const checkPluginUpdatesMutateMock = vi.fn();
const updatePluginCatalogSettingsMutateMock = vi.fn();
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
  useInstallPlugin: () => ({ mutate: vi.fn(), isPending: false }),
  useUploadPlugin: () => ({ mutate: vi.fn(), isPending: false }),
  usePluginUpload: () => ({ upload: vi.fn(), progress: null, isPending: false }),
  useUpdatePluginInstallation: () => ({ mutate: vi.fn(), isPending: false }),
  useApplyPluginUpdate: () => ({ mutate: vi.fn(), isPending: false }),
  useDeletePluginInstallation: () => ({ mutate: vi.fn(), isPending: false }),
  useSavePluginConfig: () => ({ mutate: vi.fn(), isPending: false }),
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

  it("describes manual upload as a generic plugin file instead of a zip-only archive", () => {
    const markup = renderToStaticMarkup(
      <MemoryRouter>
        <AdminPlugins />
      </MemoryRouter>,
    );

    expect(markup).toContain("Upload a plugin binary directly.");
    expect(markup).toContain("Choose plugin file...");
    expect(markup).not.toContain('accept=".zip"');
  });

  it("shows the approved community setting and explains migrated installations", () => {
    useAdminPluginsMock.mockReturnValue({
      repositories: [],
      catalog: [],
      installations: [],
      catalogSettings: {
        include_approved_community_plugins: true,
        approved_community_plugin_count: 2,
        installed_community_plugin_count: 2,
        migrated_plugin_count: 2,
        community_updates_paused: false,
      },
      isLoading: false,
    });

    const markup = renderToStaticMarkup(
      <MemoryRouter>
        <AdminPlugins />
      </MemoryRouter>,
    );

    expect(markup).toContain("Include approved community plugins");
    expect(markup).toContain("2 existing installations were moved here");
    expect(capturedSwitchProps[0]?.checked).toBe(true);
  });

  it("enables the approved community catalog directly when no community plugins are installed", () => {
    useAdminPluginsMock.mockReturnValue({
      repositories: [],
      catalog: [],
      installations: [],
      catalogSettings: {
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
      include_approved_community_plugins: true,
    });
  });

  it("does not disable the community catalog without confirmation when plugins are installed", () => {
    useAdminPluginsMock.mockReturnValue({
      repositories: [],
      catalog: [],
      installations: [],
      catalogSettings: {
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

  it("shows catalog presentation metadata and external resource links", () => {
    useAdminPluginsMock.mockReturnValue({
      repositories: [],
      installations: [],
      catalogSettings: undefined,
      isLoading: false,
      catalog: [
        {
          repository_id: 1,
          plugin_id: "silo.example",
          version: "1.0.0",
          archive_url: "https://example.com/plugin",
          source_kind: "silo",
          repository_name: "Silo plugins",
          repo_url: "https://github.com/Silo-Server/example-plugin",
          presentation: {
            display_name: "Example Plugin",
            summary: "Explains the example for a homelab administrator.",
            description_markdown: "Longer description.",
            setup_markdown: "Configure the example.",
            homepage_url: "https://example.com",
            source_url: "https://github.com/Silo-Server/example-plugin",
            support_url: "https://github.com/Silo-Server/example-plugin/issues",
            changelog_url: "https://github.com/Silo-Server/example-plugin/releases",
            publisher_name: "Silo",
            publisher_url: "https://github.com/Silo-Server",
            license_spdx: "AGPL-3.0-or-later",
          },
          capabilities: [],
          global_config_schema: [],
          user_config_schema: [],
          routes: [],
          assets: [],
        },
      ],
    });

    const markup = renderToStaticMarkup(
      <MemoryRouter>
        <AdminPlugins />
      </MemoryRouter>,
    );

    expect(markup).toContain("Example Plugin");
    expect(markup).toContain("Explains the example for a homelab administrator.");
    expect(markup).toContain('href="https://github.com/Silo-Server/example-plugin"');
    expect(markup).toContain('href="https://github.com/Silo-Server/example-plugin/releases"');
  });

  it("uses catalog presentation metadata for an older installed manifest", () => {
    useAdminPluginsMock.mockReturnValue({
      repositories: [],
      catalogSettings: undefined,
      isLoading: false,
      installations: [
        {
          id: 7,
          repository_id: 1,
          plugin_id: "silo.example",
          version: "0.9.0",
          install_path: "/plugins/example",
          enabled: true,
          source_kind: "silo",
          repository_name: "Silo plugins",
          updates_paused: false,
          capabilities: [],
          global_config_schema: [],
          user_config_schema: [],
          routes: [],
          assets: [],
          global_configs: [],
          auth_bindings: [],
          task_bindings: [],
          update_policy: "auto",
        },
      ],
      catalog: [
        {
          repository_id: 1,
          plugin_id: "silo.example",
          version: "1.0.0",
          archive_url: "https://example.com/plugin",
          source_kind: "silo",
          repository_name: "Silo plugins",
          repo_url: "https://github.com/Silo-Server/example-plugin",
          presentation: {
            display_name: "Example Plugin",
            summary: "Catalog fallback description.",
            description_markdown: "Longer description.",
            setup_markdown: "Configure the example.",
            homepage_url: "https://example.com",
            source_url: "https://github.com/Silo-Server/example-plugin",
            support_url: "https://github.com/Silo-Server/example-plugin/issues",
            changelog_url: "https://github.com/Silo-Server/example-plugin/releases",
            publisher_name: "Silo",
            publisher_url: "https://github.com/Silo-Server",
            license_spdx: "AGPL-3.0-or-later",
          },
          capabilities: [],
          global_config_schema: [],
          user_config_schema: [],
          routes: [],
          assets: [],
        },
      ],
    });

    const markup = renderToStaticMarkup(
      <MemoryRouter>
        <AdminPlugins />
      </MemoryRouter>,
    );

    expect(markup).toContain("Catalog fallback description.");
    expect(markup).toContain('href="https://github.com/Silo-Server/example-plugin"');
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

  it("paginates the catalog from URL state", () => {
    useAdminPluginsMock.mockReturnValue({
      repositories: [],
      installations: [],
      catalogSettings: undefined,
      isLoading: false,
      catalog: Array.from({ length: 13 }, (_, index) => makeCatalogEntry(index + 1)),
    });

    const markup = renderToStaticMarkup(
      <MemoryRouter initialEntries={["/admin/plugins?tab=catalog&catalog_page=2"]}>
        <AdminPlugins />
      </MemoryRouter>,
    );

    expect(markup).toContain("Plugin 13");
    expect(markup).not.toContain("Plugin 01");
    expect(markup).toContain("Showing");
    expect(markup).toContain(">13</span>–<span");
    expect(markup).toContain("2 / 2");
  });
});
