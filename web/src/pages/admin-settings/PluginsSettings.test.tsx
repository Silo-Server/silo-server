import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router";
import { describe, expect, it, vi } from "vitest";

import PluginsSettings from "./PluginsSettings";
import Login from "@/pages/Login";

const useAdminPluginsMock = vi.fn();
const useAuthMock = vi.fn();
const installPluginMutateMock = vi.fn();
const savePluginConfigMutateMock = vi.fn();
const testPluginConfigMutateAsyncMock = vi.fn();
const capturedButtonProps: Array<Record<string, unknown>> = [];

vi.mock("@/components/ui/button", () => ({
  Button: (props: Record<string, unknown>) => {
    capturedButtonProps.push(props);
    return props.children;
  },
}));

vi.mock("@/hooks/queries/admin/plugins", () => ({
  useAdminPlugins: () => useAdminPluginsMock(),
  useCreatePluginRepository: () => ({ mutate: vi.fn(), isPending: false }),
  useUpdatePluginRepository: () => ({ mutate: vi.fn(), isPending: false }),
  useDeletePluginRepository: () => ({ mutate: vi.fn(), isPending: false }),
  useInstallPlugin: () => ({ mutate: installPluginMutateMock, isPending: false }),
  useUploadPlugin: () => ({ mutate: vi.fn(), isPending: false }),
  usePluginUpload: () => ({ upload: vi.fn(), progress: null, isPending: false }),
  useUpdatePluginInstallation: () => ({ mutate: vi.fn(), isPending: false }),
  useDeletePluginInstallation: () => ({ mutate: vi.fn(), isPending: false }),
  useSavePluginConfig: () => ({ mutate: savePluginConfigMutateMock, isPending: false }),
  useTestPluginConfig: () => ({ mutateAsync: testPluginConfigMutateAsyncMock, isPending: false }),
  useSavePluginAuthBinding: () => ({ mutate: vi.fn(), isPending: false }),
  useSavePluginTaskBinding: () => ({ mutate: vi.fn(), isPending: false }),
}));

vi.mock("@/hooks/queries/admin/subtitles", () => ({
  useSubtitleProviders: () => ({ data: { providers: [] }, isLoading: false }),
  useUpdateSubtitleProvider: () => ({ mutate: vi.fn(), isPending: false }),
  useTestSubtitleProvider: () => ({ mutate: vi.fn(), isPending: false }),
}));

vi.mock("@/hooks/useSettingsForm", () => ({
  useSettingsForm: () => ({
    isLoading: false,
    getValue: () => "",
    setValue: vi.fn(),
    dirtyCount: 0,
    save: vi.fn(),
    discard: vi.fn(),
    isSaving: false,
    restartRequired: false,
    sensitiveConfigured: [],
  }),
}));

vi.mock("@/hooks/useAuth", () => ({
  useAuth: () => useAuthMock(),
}));

vi.mock("@/hooks/useServerBranding", () => ({
  useServerBranding: () => ({
    serverName: "Silo",
    loginTitle: "Sign in",
    loginSubtitle: "",
    loginBadge: "",
    heroImageUrl: "",
  }),
}));

describe("PluginsSettings", () => {
  it("installs catalog plugins by repository identity instead of archive url", () => {
    capturedButtonProps.length = 0;
    installPluginMutateMock.mockReset();

    useAdminPluginsMock.mockReturnValue({
      repositories: [],
      catalog: [
        {
          repository_id: 7,
          plugin_id: "example.remote",
          version: "1.2.3",
          archive_url: "https://plugins.example.test/example.remote.zip",
        },
      ],
      installations: [],
      isLoading: false,
    });

    renderToStaticMarkup(
      <MemoryRouter>
        <PluginsSettings />
      </MemoryRouter>,
    );

    const installButton = capturedButtonProps.find((props) => props.children === "Install");
    expect(installButton).toBeTruthy();
    expect(typeof installButton?.onClick).toBe("function");

    (installButton?.onClick as () => void)();

    expect(installPluginMutateMock).toHaveBeenCalledWith({
      repository_id: 7,
      plugin_id: "example.remote",
      version: "1.2.3",
    });
  });

  it("renders repositories, catalog entries, installations, and plugin-hosted admin links", () => {
    useAdminPluginsMock.mockReturnValue({
      repositories: [
        { id: 1, url: "https://plugins.example.test/index.json", display_name: "Example Repo" },
      ],
      catalog: [
        {
          repository_id: 1,
          plugin_id: "example.remote",
          version: "1.2.3",
          archive_url: "https://plugins.example.test/example.remote.zip",
        },
      ],
      installations: [
        {
          id: 11,
          plugin_id: "example.remote",
          version: "1.2.3",
          enabled: true,
          legacy_metadata_import_types: ["tmdb", "tvdb"],
          routes: [
            {
              id: "admin-page",
              method: "GET",
              path: "/admin",
              access: "admin",
              navigable: true,
              navigation_label: "Admin Console",
              navigation_kind: "admin",
              static_asset: false,
            },
          ],
        },
      ],
      isLoading: false,
    });

    const markup = renderToStaticMarkup(
      <MemoryRouter>
        <PluginsSettings />
      </MemoryRouter>,
    );

    expect(markup).toContain("Repositories");
    expect(markup).toContain("Example Repo");
    expect(markup).toContain("Catalog");
    expect(markup).toContain("example.remote");
    expect(markup).toContain("Installed Plugins");
    expect(markup).toContain("Admin Console");
    expect(markup).not.toContain("Import legacy");
  });

  it("does not restrict manual plugin uploads to zip files", () => {
    useAdminPluginsMock.mockReturnValue({
      repositories: [],
      catalog: [],
      installations: [],
      isLoading: false,
    });

    const markup = renderToStaticMarkup(
      <MemoryRouter>
        <PluginsSettings />
      </MemoryRouter>,
    );

    expect(markup).toContain("Upload package");
    expect(markup).not.toContain('accept=".zip"');
  });

  it("renders admin form labels instead of raw json schema blobs", () => {
    useAdminPluginsMock.mockReturnValue({
      repositories: [],
      catalog: [],
      installations: [
        {
          id: 11,
          plugin_id: "silo.tmdb",
          version: "1.0.0",
          enabled: true,
          global_configs: [{ key: "connection", value: { api_key: "secret-value" } }],
          global_config_schema: [
            {
              key: "connection",
              title: "Connection",
              description: "TMDB credentials",
              json_schema:
                '{"type":"object","properties":{"api_key":{"type":"string"}},"required":["api_key"],"additionalProperties":false}',
              required: true,
              admin_form: {
                fields: [
                  {
                    key: "api_key",
                    label: "TMDB API Key",
                    description: "Paste your TMDB API key.",
                    control: "PASSWORD",
                    required: true,
                  },
                ],
              },
            },
          ],
        },
      ],
      isLoading: false,
    });

    const markup = renderToStaticMarkup(
      <MemoryRouter>
        <PluginsSettings />
      </MemoryRouter>,
    );

    expect(markup).toContain("TMDB API Key");
    expect(markup).not.toContain("{&quot;api_key&quot;:&quot;secret-value&quot;}");
  });

  it("only shows plugin connection checks for metadata provider installations", () => {
    capturedButtonProps.length = 0;
    useAdminPluginsMock.mockReturnValue({
      repositories: [],
      catalog: [],
      installations: [
        {
          id: 11,
          plugin_id: "silo.tmdb",
          version: "1.0.0",
          enabled: true,
          capabilities: [{ type: "metadata_provider.v1", id: "tmdb", display_name: "TMDB" }],
          global_configs: [{ key: "connection", value: { api_key: "secret-value" } }],
          global_config_schema: [
            {
              key: "connection",
              title: "Connection",
              description: "TMDB credentials",
              json_schema:
                '{"type":"object","properties":{"api_key":{"type":"string"}},"required":["api_key"],"additionalProperties":false}',
              required: true,
              admin_form: {
                fields: [
                  {
                    key: "api_key",
                    label: "TMDB API Key",
                    description: "Paste your TMDB API key.",
                    control: "PASSWORD",
                    required: true,
                  },
                ],
              },
            },
          ],
        },
        {
          id: 12,
          plugin_id: "silo.jobs",
          version: "1.0.0",
          enabled: true,
          capabilities: [{ type: "scheduled_task.v1", id: "refresh", display_name: "Refresh" }],
          global_configs: [],
          global_config_schema: [
            {
              key: "task",
              title: "Task",
              json_schema: '{"type":"object","properties":{"enabled":{"type":"boolean"}}}',
              required: false,
            },
          ],
        },
      ],
      isLoading: false,
    });

    renderToStaticMarkup(
      <MemoryRouter>
        <PluginsSettings />
      </MemoryRouter>,
    );

    const checkButtons = capturedButtonProps.filter(
      (props) => props.children === "Check Connection",
    );
    expect(checkButtons).toHaveLength(1);
  });

  it("shows credential providers on the login screen", () => {
    useAuthMock.mockReturnValue({
      login: vi.fn(),
      user: null,
      loading: false,
      setupLoading: false,
      setupRequired: false,
      providers: [
        { id: "local", display_name: "Local", mode: "credentials", default: true },
        { id: "plugin:41:ldap", display_name: "LDAP", mode: "credentials", default: false },
      ],
    });

    const markup = renderToStaticMarkup(
      <MemoryRouter>
        <Login />
      </MemoryRouter>,
    );

    expect(markup).toContain("Sign in with");
    expect(markup).toContain('role="combobox"');
  });
});
