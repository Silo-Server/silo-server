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

  it("does not render a visible search input before Cmd+K", () => {
    renderDialog();

    expect(screen.queryByRole("searchbox", { name: "Search admin sections" })).toBeNull();
  });

  it("opens and focuses admin search with Cmd+K", async () => {
    renderDialog();

    await openDialog();

    expect(screen.getByRole("option", { name: /Dashboard/ })).toBeInTheDocument();
  });

  it("searches all admin section groups", async () => {
    renderDialog();

    const searchBox = await openDialog();
    await userEvent.type(searchBox, "history import");

    expect(screen.getByRole("option", { name: /History Import/ })).toBeInTheDocument();
    expect(screen.queryByRole("option", { name: /Settings/ })).not.toBeInTheDocument();
  });

  it("searches individual admin setting labels from the dashboard dialog", async () => {
    renderDialog();

    const searchBox = await openDialog();
    await userEvent.type(searchBox, "pool max open");

    expect(screen.getByRole("option", { name: /Database/ })).toBeInTheDocument();
    expect(screen.getByText("Pool Max Open")).toBeInTheDocument();

    await userEvent.click(screen.getByRole("option", { name: /Database/ }));

    expect(screen.getByLabelText("Current path")).toHaveTextContent("/admin/settings?tab=database");
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

    expect(mocks.navigateToPluginRoute).toHaveBeenCalledWith("/api/v1/plugins/7/");
    expect(screen.queryByRole("searchbox", { name: "Search admin sections" })).toBeNull();
  });

  it("closes after choosing an internal result", async () => {
    renderDialog();

    const searchBox = await openDialog();
    await userEvent.type(searchBox, "logs");
    await userEvent.click(screen.getByRole("option", { name: /Logs/ }));

    expect(screen.getByLabelText("Current path")).toHaveTextContent("/admin/logs");
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
