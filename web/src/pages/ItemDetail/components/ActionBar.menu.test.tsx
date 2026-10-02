import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import ActionBar from "./ActionBar";

vi.mock("@/playback/watchPlaybackContext", () => ({
  useWatchPlaybackController: () => ({ startPlayback: vi.fn() }),
}));

vi.mock("@/components/AddToCollectionDialog", () => ({
  default: () => null,
}));

const markerMocks = vi.hoisted(() => ({
  detectionKinds: undefined as { intro: boolean; credits: boolean } | undefined,
}));
vi.mock("@/hooks/queries/admin/markers", () => ({
  useMarkerDetectionKinds: () => markerMocks.detectionKinds,
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

describe("ActionBar marker re-detection", () => {
  afterEach(() => {
    markerMocks.detectionKinds = undefined;
  });

  it.each([
    [{ intro: true, credits: false }, ["Intro"], ["Credits", "Intro and credits"]],
    [{ intro: false, credits: true }, ["Credits"], ["Intro", "Intro and credits"]],
    [{ intro: false, credits: false }, [], ["Intro", "Credits", "Intro and credits"]],
  ])("with detection %j offers only %j", async (kinds, enabledLabels, disabledLabels) => {
    markerMocks.detectionKinds = kinds;
    render(
      <MemoryRouter>
        <ActionBar contentId="episode-1" isAdmin onRedetectMarkers={vi.fn()} />
      </MemoryRouter>,
    );

    await userEvent.click(screen.getByTitle("More"));
    await userEvent.click(screen.getByRole("menuitem", { name: "Re-detect Markers" }));
    const dialog = await screen.findByRole("dialog", { name: "Re-detect Markers" });
    for (const name of enabledLabels) {
      expect(within(dialog).getByRole("button", { name })).toBeEnabled();
    }
    for (const name of disabledLabels) {
      const option = within(dialog).getByRole("button", { name });
      expect(option).toBeDisabled();
      expect(option).toHaveAccessibleDescription(/turned off in marker settings/);
    }
    expect(within(dialog).getByRole("link", { name: "Change marker settings" })).toHaveAttribute(
      "href",
      "/admin/settings/library",
    );
  });

  it("links to marker settings only when a kind is off", async () => {
    markerMocks.detectionKinds = { intro: true, credits: true };
    render(
      <MemoryRouter>
        <ActionBar contentId="episode-1" isAdmin onRedetectMarkers={vi.fn()} />
      </MemoryRouter>,
    );

    await userEvent.click(screen.getByTitle("More"));
    await userEvent.click(screen.getByRole("menuitem", { name: "Re-detect Markers" }));
    const dialog = await screen.findByRole("dialog", { name: "Re-detect Markers" });
    for (const name of ["Intro", "Credits", "Intro and credits"]) {
      expect(within(dialog).getByRole("button", { name })).toBeEnabled();
    }
    expect(within(dialog).queryByRole("link")).toBeNull();
  });

  it("asks admins which episode markers to re-detect", async () => {
    const onRedetectMarkers = vi.fn();
    render(
      <MemoryRouter>
        <ActionBar contentId="episode-1" isAdmin onRedetectMarkers={onRedetectMarkers} />
      </MemoryRouter>,
    );

    await userEvent.click(screen.getByTitle("More"));
    expect(screen.queryByRole("menuitem", { name: "Re-detect Intro Markers" })).toBeNull();
    await userEvent.click(screen.getByRole("menuitem", { name: "Re-detect Markers" }));
    expect(onRedetectMarkers).not.toHaveBeenCalled();

    const dialog = await screen.findByRole("dialog", { name: "Re-detect Markers" });
    for (const name of ["Intro", "Credits", "Intro and credits"]) {
      expect(within(dialog).getByRole("button", { name })).toBeTruthy();
    }
    await userEvent.click(within(dialog).getByRole("button", { name: "Credits" }));
    expect(onRedetectMarkers).toHaveBeenCalledExactlyOnceWith("credits");
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  });

  it.each([
    ["Intro", "intro"],
    ["Intro and credits", "all"],
  ])("re-detects %s for an episode", async (label, kind) => {
    const onRedetectMarkers = vi.fn();
    render(
      <MemoryRouter>
        <ActionBar contentId="episode-1" isAdmin onRedetectMarkers={onRedetectMarkers} />
      </MemoryRouter>,
    );

    await userEvent.click(screen.getByTitle("More"));
    await userEvent.click(screen.getByRole("menuitem", { name: "Re-detect Markers" }));
    const dialog = await screen.findByRole("dialog", { name: "Re-detect Markers" });
    await userEvent.click(within(dialog).getByRole("button", { name: label }));
    expect(onRedetectMarkers).toHaveBeenCalledExactlyOnceWith(kind);
  });

  it("re-detects a movie's credits directly", async () => {
    const onRedetectMarkers = vi.fn();
    render(
      <MemoryRouter>
        <ActionBar
          contentId="movie-1"
          isAdmin
          redetectKind="credits"
          onRedetectMarkers={onRedetectMarkers}
        />
      </MemoryRouter>,
    );

    await userEvent.click(screen.getByTitle("More"));
    expect(screen.queryByRole("menuitem", { name: "Re-detect Markers" })).toBeNull();
    await userEvent.click(screen.getByRole("menuitem", { name: "Re-detect Credits" }));
    expect(onRedetectMarkers).toHaveBeenCalledExactlyOnceWith("credits");
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("re-detects an episode intro directly when only the intro operation exists", async () => {
    const onRedetectMarkers = vi.fn();
    render(
      <MemoryRouter>
        <ActionBar
          contentId="episode-1"
          isAdmin
          redetectKind="intro"
          onRedetectMarkers={onRedetectMarkers}
        />
      </MemoryRouter>,
    );

    await userEvent.click(screen.getByTitle("More"));
    expect(screen.queryByRole("menuitem", { name: "Re-detect Markers" })).toBeNull();
    await userEvent.click(screen.getByRole("menuitem", { name: "Re-detect Intro Markers" }));
    expect(onRedetectMarkers).toHaveBeenCalledExactlyOnceWith("intro");
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("hides re-detection from non-admins", async () => {
    render(
      <MemoryRouter>
        <ActionBar contentId="episode-1" onToggleWatchlist={() => {}} onRedetectMarkers={vi.fn()} />
      </MemoryRouter>,
    );

    await userEvent.click(screen.getByTitle("More"));
    expect(screen.queryByRole("menuitem", { name: /Re-detect/ })).toBeNull();
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

  it("offers Request Seasons only with the prop", async () => {
    const onRequestSeasons = vi.fn();
    render(
      <MemoryRouter>
        <ActionBar contentId="series-1" onRequestSeasons={onRequestSeasons} />
      </MemoryRouter>,
    );
    await userEvent.click(screen.getByTitle("More"));
    await userEvent.click(screen.getByRole("menuitem", { name: "Request Seasons" }));
    expect(onRequestSeasons).toHaveBeenCalledTimes(1);
  });
});
