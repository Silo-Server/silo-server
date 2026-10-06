import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, useLocation } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { PluginInstallation } from "@/api/types";
import { buildAdminCommandNavSections } from "@/lib/adminNavigation";

const mocks = vi.hoisted(() => ({
  navigateToPluginRoute: vi.fn(),
}));

vi.mock("@/lib/buildPluginHref", () => ({
  navigateToPluginRoute: (...args: unknown[]) => mocks.navigateToPluginRoute(...args),
}));

import { AdminSectionCommandDialog } from "./AdminSectionCommandDialog";

function renderDialog(sections = buildAdminCommandNavSections(undefined)) {
  render(
    <MemoryRouter initialEntries={["/admin"]}>
      <AdminSectionCommandDialog sections={sections} />
      <CurrentPath />
    </MemoryRouter>,
  );
}

function CurrentPath() {
  const location = useLocation();
  return <output aria-label="Current path">{`${location.pathname}${location.search}`}</output>;
}

async function openDialog() {
  fireEvent.keyDown(window, { key: "k", metaKey: true });
  const searchBox = await screen.findByRole("searchbox", { name: "Search admin sections" });
  await waitFor(() => expect(searchBox).toHaveFocus());
  return searchBox;
}

describe("AdminSectionCommandDialog", () => {
  beforeEach(() => {
    mocks.navigateToPluginRoute.mockReset();
  });

  it("opens and focuses admin search with Cmd+K", async () => {
    renderDialog();

    await openDialog();

    expect(screen.getByRole("option", { name: /Dashboard/ })).toBeInTheDocument();
  });

  it("opens and focuses admin search with Ctrl+K", async () => {
    renderDialog();

    fireEvent.keyDown(window, { key: "k", ctrlKey: true });
    const searchBox = await screen.findByRole("searchbox", { name: "Search admin sections" });

    await waitFor(() => expect(searchBox).toHaveFocus());
    expect(screen.getByRole("option", { name: /Dashboard/ })).toBeInTheDocument();
  });

  it("searches individual admin setting labels from the admin dialog", async () => {
    renderDialog();

    const searchBox = await openDialog();
    await userEvent.type(searchBox, "maximum postgres connections");

    expect(screen.getByRole("option", { name: /Storage & Database/ })).toBeInTheDocument();
    expect(screen.getByText("Maximum Postgres connections")).toBeInTheDocument();

    await userEvent.click(screen.getByRole("option", { name: /Storage & Database/ }));

    expect(screen.getByLabelText("Current path")).toHaveTextContent(
      "/admin/settings/infrastructure",
    );
    expect(screen.queryByRole("searchbox", { name: "Search admin sections" })).toBeNull();
  });

  it("includes admin plugin app destinations", async () => {
    const sections = buildAdminCommandNavSections([
      {
        id: 7,
        plugin_id: "arrproxy",
        enabled: true,
        routes: [
          {
            id: "admin",
            method: "GET",
            path: "/",
            access: "admin",
            navigable: true,
            navigation_label: "ArrProxy",
            navigation_kind: "admin",
            static_asset: true,
          },
        ],
      } as PluginInstallation,
    ]);
    renderDialog(sections);

    const searchBox = await openDialog();
    await userEvent.type(searchBox, "arrproxy");
    await userEvent.click(screen.getByRole("option", { name: /ArrProxy/ }));

    expect(mocks.navigateToPluginRoute).toHaveBeenCalledWith("/api/v2/plugin-content/plugins/7/");
    expect(screen.queryByRole("searchbox", { name: "Search admin sections" })).toBeNull();
  });

  it("closes with Escape", async () => {
    renderDialog();

    await openDialog();
    await userEvent.keyboard("{Escape}");

    await waitFor(() =>
      expect(screen.queryByRole("searchbox", { name: "Search admin sections" })).toBeNull(),
    );
  });

  it("captures Cmd+K before document-level global search handlers", async () => {
    const globalSearchShortcut = vi.fn();
    document.addEventListener("keydown", globalSearchShortcut);

    try {
      renderDialog();

      fireEvent.keyDown(document.body, { key: "k", metaKey: true });

      await screen.findByRole("searchbox", { name: "Search admin sections" });
      expect(globalSearchShortcut).not.toHaveBeenCalled();
    } finally {
      document.removeEventListener("keydown", globalSearchShortcut);
    }
  });
});
