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
    expect(menu).toHaveClass("w-max", "max-w-[calc(100vw-2rem)]", "min-w-0");
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

describe("ActionBar watch together group", () => {
  it("shows the group only with the prop, and live-room items only with a live room", async () => {
    const onStartParty = vi.fn();
    const onSuggest = vi.fn();
    const onPlay = vi.fn();
    const view = render(
      <MemoryRouter>
        <ActionBar contentId="movie-1" watchTogether={{ onStartParty }} />
      </MemoryRouter>,
    );
    await userEvent.click(screen.getByTitle("More"));
    expect(screen.getByText("Watch Together")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("menuitem", { name: "Start a party with this" }));
    expect(onStartParty).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole("menuitem", { name: /Suggest to/ })).toBeNull();
    view.unmount();

    render(
      <MemoryRouter>
        <ActionBar
          contentId="movie-1"
          watchTogether={{ onStartParty, liveRoom: { code: "KX7Q2M", onSuggest, onPlay } }}
        />
      </MemoryRouter>,
    );
    await userEvent.click(screen.getByTitle("More"));
    expect(screen.getByText(/KX7Q2M is live/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("menuitem", { name: "Suggest to KX7Q2M" }));
    expect(onSuggest).toHaveBeenCalledTimes(1);
    await userEvent.click(screen.getByTitle("More"));
    await userEvent.click(screen.getByRole("menuitem", { name: "Play in KX7Q2M" }));
    expect(onPlay).toHaveBeenCalledTimes(1);
    for (const item of screen.queryAllByRole("menuitem")) {
      expect(item.querySelector("svg")).toBeTruthy();
    }
  });
});
