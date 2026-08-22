import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { describe, expect, it, vi } from "vitest";
import ActionBar from "./ActionBar";

vi.mock("@/playback/watchPlaybackContext", () => ({
  useWatchPlaybackController: () => ({ startPlayback: vi.fn() }),
}));

vi.mock("@/components/AddToCollectionDialog", () => ({
  default: () => null,
}));

describe("ActionBar detail menu", () => {
  it("uses matching icons and longest-entry sizing", async () => {
    render(
      <MemoryRouter>
        <ActionBar
          contentId="series-1"
          isAdmin
          canCurateMetadata
          onToggleWatchlist={() => {}}
          onRefresh={() => {}}
          onEditMetadata={() => {}}
          onMatchItem={() => {}}
          onSplitItem={() => {}}
        />
      </MemoryRouter>,
    );

    await userEvent.click(screen.getByTitle("More"));

    const menu = screen.getByRole("menu");
    expect(menu).toHaveClass("w-max", "min-w-0");
    expect(menu).not.toHaveClass("w-56");
    for (const item of screen.getAllByRole("menuitem")) {
      expect(item.querySelector("svg"), item.textContent ?? "menu item").toBeTruthy();
    }
    expect(
      screen.getByRole("menuitem", { name: "View Play History" }).querySelector(".lucide-history"),
    ).toBeTruthy();
    expect(
      screen
        .getByRole("menuitem", { name: "Refresh Metadata" })
        .querySelector(".lucide-refresh-cw"),
    ).toBeTruthy();
  });
});
