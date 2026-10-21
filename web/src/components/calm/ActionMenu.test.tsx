import { cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Clapperboard, FolderOpen, Pencil, Trash2 } from "lucide-react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ActionMenu } from "./ActionMenu";

afterEach(cleanup);

describe("ActionMenu", () => {
  it("opens a submenu with → and runs the chosen item", async () => {
    const openMovies = vi.fn();
    const remove = vi.fn();
    render(
      <ActionMenu
        label="More for Studio Ghibli"
        items={[
          { key: "edit", label: "Edit collection", icon: Pencil, onSelect: vi.fn() },
          {
            key: "open",
            label: "Open in",
            icon: FolderOpen,
            items: [
              { key: "movies", label: "Movies", icon: Clapperboard, onSelect: openMovies },
              { key: "kids", label: "Kids", icon: Clapperboard, onSelect: vi.fn() },
            ],
          },
          {
            key: "delete",
            label: "Delete…",
            icon: Trash2,
            destructive: true,
            group: true,
            onSelect: remove,
          },
        ]}
      />,
    );
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "More for Studio Ghibli" }));
    const menu = await screen.findByRole("menu");
    expect(within(menu).getAllByRole("separator")).toHaveLength(1);
    const openIn = within(menu).getByRole("menuitem", { name: "Open in" });
    expect(openIn).toHaveAttribute("aria-haspopup", "menu");
    openIn.focus();
    await user.keyboard("{ArrowRight}");
    const movies = await screen.findByRole("menuitem", { name: "Movies" });
    await user.click(movies);
    expect(openMovies).toHaveBeenCalledTimes(1);
    expect(remove).not.toHaveBeenCalled();
  });

  it("reads a setting as a checked item with its help, and reports the new value", async () => {
    const onCheckedChange = vi.fn();
    render(
      <ActionMenu
        label="More for Rainy days"
        items={[
          {
            key: "share",
            label: "Show to other profiles",
            help: "Every profile on this account sees it",
            icon: Pencil,
            checked: false,
            onCheckedChange,
          },
        ]}
      />,
    );
    await userEvent.click(screen.getByRole("button", { name: "More for Rainy days" }));
    const toggle = await screen.findByRole("menuitemcheckbox", {
      name: "Show to other profiles",
      description: "Every profile on this account sees it",
    });
    expect(toggle).toHaveAttribute("aria-checked", "false");
    await userEvent.click(toggle);
    expect(onCheckedChange).toHaveBeenCalledWith(true);
  });
});
