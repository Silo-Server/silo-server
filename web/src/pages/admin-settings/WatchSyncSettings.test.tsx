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
