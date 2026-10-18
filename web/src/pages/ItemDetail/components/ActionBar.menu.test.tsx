import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import ActionBar from "./ActionBar";

vi.mock("@/playback/watchPlaybackContext", () => ({
  useWatchPlaybackController: () => ({ startPlayback: vi.fn() }),
}));

const addToCollection = vi.hoisted(() => ({
  props: null as null | { open: boolean; mediaItemId: string; itemTitle?: string },
}));
vi.mock("@/components/AddToCollectionDialog", () => ({
  default: (props: { open: boolean; mediaItemId: string; itemTitle?: string }) => {
    addToCollection.props = props;
    return null;
  },
}));

const markerMocks = vi.hoisted(() => ({
  detectionKinds: undefined as { intro: boolean; credits: boolean } | undefined,
}));
vi.mock("@/hooks/queries/admin/markers", () => ({
  useMarkerDetectionKinds: () => markerMocks.detectionKinds,
}));

async function openManage() {
  await userEvent.click(screen.getByTitle("More"));
  await userEvent.click(screen.getByRole("menuitem", { name: "Manage" }));
}

describe("ActionBar Add to collection", () => {
  it("names the title it adds", async () => {
    render(
      <MemoryRouter>
        <ActionBar contentId="movie-1" itemTitle="Heat" />
      </MemoryRouter>,
    );

    await userEvent.click(screen.getByTitle("More"));
    await userEvent.click(screen.getByRole("menuitem", { name: "Add to Collection" }));
    expect(addToCollection.props).toMatchObject({
      open: true,
      mediaItemId: "movie-1",
      itemTitle: "Heat",
    });
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

    await openManage();
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
      "/admin/settings/playback",
    );
  });

  it("links to marker settings only when a kind is off", async () => {
    markerMocks.detectionKinds = { intro: true, credits: true };
    render(
      <MemoryRouter>
        <ActionBar contentId="episode-1" isAdmin onRedetectMarkers={vi.fn()} />
      </MemoryRouter>,
    );

    await openManage();
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

    await openManage();
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

    await openManage();
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

    await openManage();
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

    await openManage();
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

describe("ActionBar request and party actions", () => {
  it("dispatches Watch Together and Request Seasons menu actions", async () => {
    const onRequestSeasons = vi.fn();
    const onStartParty = vi.fn();
    const onSuggest = vi.fn();
    const onPlay = vi.fn();
    const view = render(
      <MemoryRouter>
        <ActionBar
          contentId="movie-1"
          watchTogether={{ onStartParty }}
          onRequestSeasons={onRequestSeasons}
        />
      </MemoryRouter>,
    );
    await userEvent.click(screen.getByTitle("More"));
    expect(screen.queryByRole("menuitem", { name: /Suggest to/ })).toBeNull();
    await userEvent.click(screen.getByRole("menuitem", { name: "Request Seasons" }));
    expect(onRequestSeasons).toHaveBeenCalledTimes(1);
    await userEvent.click(screen.getByTitle("More"));
    await userEvent.click(screen.getByRole("menuitem", { name: "Start a Watch Party" }));
    expect(onStartParty).toHaveBeenCalledTimes(1);
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
    await userEvent.click(screen.getByRole("menuitem", { name: "Suggest to KX7Q2M" }));
    expect(onSuggest).toHaveBeenCalledTimes(1);
    await userEvent.click(screen.getByTitle("More"));
    await userEvent.click(screen.getByRole("menuitem", { name: "Play in KX7Q2M" }));
    expect(onPlay).toHaveBeenCalledTimes(1);
  });
});

describe("ActionBar shuffle", () => {
  it("offers Shuffle first in the menu and starts it", async () => {
    const onShuffle = vi.fn();
    render(
      <MemoryRouter>
        <ActionBar contentId="series-1" playHref="/watch/episode-1" onShuffle={onShuffle} />
      </MemoryRouter>,
    );

    await userEvent.click(screen.getByTitle("More"));
    const items = screen.getAllByRole("menuitem");
    expect(items[0]).toHaveAccessibleName("Shuffle");
    await userEvent.click(items[0]!);

    expect(onShuffle).toHaveBeenCalledOnce();
    expect(screen.queryByRole("menu")).toBeNull();
  });

  it("leaves Shuffle out when the page cannot shuffle", async () => {
    render(
      <MemoryRouter>
        <ActionBar contentId="season-1" playHref="/watch/episode-1" />
      </MemoryRouter>,
    );

    await userEvent.click(screen.getByTitle("More"));
    expect(screen.queryByRole("menuitem", { name: "Shuffle" })).toBeNull();
  });
});

describe("ActionBar reset progress", () => {
  it("leaves Play from Beginning to the Play button's resume prompt", async () => {
    const onResetProgress = vi.fn();
    render(
      <MemoryRouter>
        <ActionBar
          contentId="movie-1"
          playHref="/watch/movie-1"
          playLabel="Resume"
          restartHref="/watch/movie-1?restart=1"
          onResetProgress={onResetProgress}
        />
      </MemoryRouter>,
    );

    await userEvent.click(screen.getByTitle("More"));
    expect(screen.queryByRole("menuitem", { name: "Play from Beginning" })).toBeNull();
    await userEvent.click(screen.getByRole("menuitem", { name: "Reset Progress" }));

    expect(onResetProgress).toHaveBeenCalledOnce();
    expect(screen.queryByRole("menu")).toBeNull();
  });

  it("offers Play from Beginning before Reset Progress when Play does not ask", async () => {
    render(
      <MemoryRouter>
        <ActionBar
          contentId="movie-1"
          playHref="/watch/movie-1"
          playLabel="Resume"
          restartHref="/watch/movie-1?restart=1"
          versions={[{ file_id: 1 }, { file_id: 2 }] as never}
          onResetProgress={vi.fn()}
        />
      </MemoryRouter>,
    );

    await userEvent.click(screen.getByTitle("More"));
    const labels = screen.getAllByRole("menuitem").map((item) => item.textContent);
    expect(labels.slice(0, 2)).toEqual(["Play from Beginning", "Reset Progress"]);
  });

  it("leaves Reset Progress out without a resume position", async () => {
    render(
      <MemoryRouter>
        <ActionBar contentId="movie-1" playHref="/watch/movie-1" />
      </MemoryRouter>,
    );

    await userEvent.click(screen.getByTitle("More"));
    expect(screen.queryByRole("menuitem", { name: "Reset Progress" })).toBeNull();
  });
});

describe("ActionBar collection eligibility", () => {
  it.each(["movie", "series"])("keeps Add to Collection for %s", async (type) => {
    render(
      <MemoryRouter>
        <ActionBar contentId={`${type}-1`} />
      </MemoryRouter>,
    );
    await userEvent.click(screen.getByTitle("More"));
    expect(screen.getByRole("menuitem", { name: "Add to Collection" })).toBeInTheDocument();
  });

  it.each(["season", "episode"])("hides Add to Collection for %s", async (type) => {
    render(
      <MemoryRouter>
        <ActionBar
          contentId={`${type}-1`}
          canAddToCollection={false}
          isAdmin
          watchedLabel="Mark Watched"
          onToggleWatched={vi.fn()}
        />
      </MemoryRouter>,
    );
    await userEvent.click(screen.getByTitle("More"));
    expect(screen.queryByRole("menuitem", { name: "Add to Collection" })).toBeNull();
    expect(screen.getByRole("button", { name: "Mark Watched" })).toBeInTheDocument();
  });

  it("omits an empty overflow menu when collection membership is unavailable", () => {
    render(
      <MemoryRouter>
        <ActionBar contentId="season-1" canAddToCollection={false} />
      </MemoryRouter>,
    );
    expect(screen.queryByTitle("More")).toBeNull();
  });
});

describe("ActionBar Manage menu", () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  function renderAdmin() {
    const onEditMetadata = vi.fn();
    render(
      <MemoryRouter>
        <ActionBar
          contentId="movie-1"
          isAdmin
          canCurateMetadata
          onToggleWatchlist={vi.fn()}
          onEditMetadata={onEditMetadata}
          onMatchItem={vi.fn()}
        />
      </MemoryRouter>,
    );
    return { onEditMetadata };
  }

  // jsdom has no layout; the menus only move focus to items that render a box.
  function mockLayout() {
    vi.spyOn(HTMLElement.prototype, "getClientRects").mockReturnValue([
      new DOMRect(0, 0, 100, 30),
    ] as unknown as DOMRectList);
  }

  it("opens management actions in a second menu beside the first", async () => {
    const { onEditMetadata } = renderAdmin();

    await userEvent.click(screen.getByTitle("More"));
    expect(screen.queryByRole("menuitem", { name: "Edit Metadata" })).toBeNull();
    const manage = screen.getByRole("menuitem", { name: "Manage" });
    expect(manage).toHaveAttribute("aria-expanded", "false");
    await userEvent.click(manage);

    expect(manage).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByRole("menuitem", { name: "Add to Watchlist" })).toBeInTheDocument();
    const submenu = screen.getByRole("menu", { name: "Manage" });
    const labels = within(submenu)
      .getAllByRole("menuitem")
      .map((item) => item.textContent);
    expect(labels).toEqual(["Edit Metadata", "Match Item", "View Play History"]);
    await userEvent.click(within(submenu).getByRole("menuitem", { name: "Edit Metadata" }));
    expect(onEditMetadata).toHaveBeenCalledOnce();
    expect(screen.queryByRole("menu")).toBeNull();
  });

  it("opens on hover and closes when another item is hovered", async () => {
    renderAdmin();

    await userEvent.click(screen.getByTitle("More"));
    await userEvent.hover(screen.getByRole("menuitem", { name: "Manage" }));
    expect(screen.getByRole("menu", { name: "Manage" })).toBeInTheDocument();
    await userEvent.hover(screen.getByRole("menuitem", { name: "Add to Watchlist" }));
    expect(screen.queryByRole("menu", { name: "Manage" })).toBeNull();
  });

  it.each(["{ArrowLeft}", "{Escape}"])(
    "moves into Manage with ArrowRight and back to it with %s",
    async (key) => {
      mockLayout();
      renderAdmin();

      await userEvent.click(screen.getByTitle("More"));
      const manage = screen.getByRole("menuitem", { name: "Manage" });
      manage.focus();
      await userEvent.keyboard("{ArrowRight}");
      const submenu = screen.getByRole("menu", { name: "Manage" });
      await waitFor(() =>
        expect(within(submenu).getByRole("menuitem", { name: "Edit Metadata" })).toHaveFocus(),
      );

      await userEvent.keyboard(key);
      expect(screen.queryByRole("menu", { name: "Manage" })).toBeNull();
      expect(manage).toHaveFocus();
      expect(manage).toHaveAttribute("aria-expanded", "false");
    },
  );

  it("keeps Manage beside its trigger when the main menu scrolls", async () => {
    renderAdmin();
    await userEvent.click(screen.getByTitle("More"));
    const manage = screen.getByRole("menuitem", { name: "Manage" });
    let triggerTop = 200;
    vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (
      this: HTMLElement,
    ) {
      return this === manage ? new DOMRect(0, triggerTop, 120, 30) : new DOMRect(0, 0, 120, 100);
    });
    await userEvent.click(manage);
    const submenu = screen.getByRole("menu", { name: "Manage" });
    await waitFor(() => expect(submenu.style.top).toBe("195px"));

    triggerTop = 100;
    const mainMenu = screen.getAllByRole("menu").find((menu) => menu !== submenu)!;
    fireEvent.scroll(mainMenu);
    await waitFor(() => expect(submenu.style.top).toBe("95px"));
  });

  it("shows a lone management action in the main menu", async () => {
    render(
      <MemoryRouter>
        <ActionBar contentId="movie-1" isAdmin onToggleWatchlist={vi.fn()} />
      </MemoryRouter>,
    );

    await userEvent.click(screen.getByTitle("More"));
    expect(screen.queryByRole("menuitem", { name: "Manage" })).toBeNull();
    expect(screen.getByRole("menuitem", { name: "View Play History" })).toBeInTheDocument();
  });
});
