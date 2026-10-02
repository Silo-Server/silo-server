import { render as renderDOM, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { PluginInstallation } from "@/api/types";

import WatchSyncSettings from "./WatchSyncSettings";

let pluginInstallations: Partial<PluginInstallation>[] | undefined = [];
let installationsLoading = false;
let installationsError = false;
const refetchInstallations = vi.fn();

vi.mock("@/hooks/queries/admin/plugins", () => ({
  useAdminPluginInstallations: () => ({
    data: pluginInstallations,
    isLoading: installationsLoading,
    isError: installationsError,
    refetch: refetchInstallations,
  }),
}));

function render(ui: React.ReactElement) {
  return renderDOM(<MemoryRouter>{ui}</MemoryRouter>);
}

describe("WatchSyncSettings", () => {
  beforeEach(() => {
    pluginInstallations = [];
    installationsLoading = false;
    installationsError = false;
    refetchInstallations.mockReset();
  });

  it("reports a failed load instead of claiming nothing is installed", async () => {
    pluginInstallations = undefined;
    installationsError = true;
    render(<WatchSyncSettings />);

    expect(screen.getByRole("alert")).toHaveTextContent(
      "Couldn't load the installed watch provider plugins.",
    );
    expect(screen.queryByText("No watch provider plugins are installed.")).not.toBeInTheDocument();
    await userEvent.setup().click(screen.getByRole("button", { name: "Retry" }));
    expect(refetchInstallations).toHaveBeenCalled();
  });

  it("heads the page and says when no provider is installed", () => {
    render(<WatchSyncSettings />);

    expect(screen.getByRole("heading", { level: 1, name: "Watch Providers" })).toBeInTheDocument();
    expect(screen.getByText("No watch provider plugins are installed.")).toBeInTheDocument();
    // Trakt and Simkl are plugins now; the page no longer edits app credentials.
    expect(screen.queryByRole("group", { name: "Trakt" })).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Client ID")).not.toBeInTheDocument();
    expect(screen.getByRole("link", { name: "plugin catalog" })).toHaveAttribute(
      "href",
      "/admin/plugins?tab=catalog",
    );
  });

  it("shows a loading state while installations load", () => {
    installationsLoading = true;
    render(<WatchSyncSettings />);

    expect(screen.getByRole("status", { name: "Loading watch sync" })).toBeInTheDocument();
  });

  it("lists installed watch-provider plugins", () => {
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
});
