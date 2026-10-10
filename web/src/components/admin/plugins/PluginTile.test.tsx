import { cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { PluginCatalogEntry } from "@/api/types";
import { tierNotice } from "@/lib/pluginPresentation";
import type { PluginSourceKind } from "@/lib/pluginPresentation";

import { CatalogPluginTile } from "./PluginTile";

const installPluginMutateMock = vi.fn();

vi.mock("@/hooks/queries/admin/plugins", () => ({
  useInstallPlugin: () => ({ mutate: installPluginMutateMock, isPending: false }),
}));

function makeEntry(sourceKind: PluginSourceKind): PluginCatalogEntry {
  return {
    repository_id: 7,
    plugin_id: "example.subtitles",
    version: "2.1.0",
    archive_url: "https://example.test/plugin.tar.gz",
    source_kind: sourceKind,
    repository_name: "Example plugins",
    repo_url: "https://example.test/plugins",
    presentation: {
      display_name: "Example Subtitles",
      summary: "Finds subtitles.",
      description_markdown: "",
      setup_markdown: "",
      homepage_url: "",
      source_url: "",
      support_url: "",
      changelog_url: "",
      publisher_name: "Example",
      publisher_url: "",
      license_spdx: "MIT",
    },
    capabilities: [],
    global_config_schema: [],
    user_config_schema: [],
    routes: [],
    assets: [],
  };
}

function renderTile(sourceKind: PluginSourceKind) {
  render(
    <MemoryRouter>
      <CatalogPluginTile entry={makeEntry(sourceKind)} isInstalled={false} />
    </MemoryRouter>,
  );
}

const expectedInstall = { repository_id: 7, plugin_id: "example.subtitles", version: "2.1.0" };

describe("CatalogPluginTile install", () => {
  beforeEach(() => installPluginMutateMock.mockReset());
  afterEach(cleanup);

  it.each<PluginSourceKind>(["approved_community", "external"])(
    "shows the %s tier notice and installs only after confirming",
    async (sourceKind) => {
      const user = userEvent.setup();
      renderTile(sourceKind);

      await user.click(screen.getByRole("button", { name: "Install Example Subtitles" }));
      expect(installPluginMutateMock).not.toHaveBeenCalled();

      const dialog = screen.getByRole("alertdialog", { name: "Install Example Subtitles?" });
      expect(dialog).toHaveTextContent(tierNotice(sourceKind)!);

      await user.click(within(dialog).getByRole("button", { name: "Install plugin" }));
      expect(installPluginMutateMock).toHaveBeenCalledTimes(1);
      expect(installPluginMutateMock).toHaveBeenCalledWith(expectedInstall);
    },
  );

  it("does not install when the admin cancels", async () => {
    const user = userEvent.setup();
    renderTile("external");

    await user.click(screen.getByRole("button", { name: "Install Example Subtitles" }));
    const dialog = screen.getByRole("alertdialog");
    await user.click(within(dialog).getByRole("button", { name: "Cancel" }));

    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
    expect(installPluginMutateMock).not.toHaveBeenCalled();
  });

  it("installs a Silo plugin directly", async () => {
    const user = userEvent.setup();
    renderTile("silo");

    await user.click(screen.getByRole("button", { name: "Install Example Subtitles" }));

    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
    expect(installPluginMutateMock).toHaveBeenCalledWith(expectedInstall);
  });
});
