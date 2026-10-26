import { act, useRef, type ComponentProps } from "react";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import type { ItemDetail, MediaItemUserState } from "@/api/types";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import MediaItemMenu, { buildMediaItemMenuModel, MetadataActionDialogHost } from "./MediaItemMenu";

const mocks = vi.hoisted(() => ({
  useCatalogItemDetail: vi.fn(),
  editItem: vi.fn(),
  matchItem: vi.fn(),
  toggleFavorite: vi.fn(),
  toggleWatched: vi.fn(),
  posterSize: "standard" as "compact" | "standard" | "large",
  authState: undefined as { user: { role: "admin" | "user" } } | undefined,
}));

vi.mock("@/hooks/queries/catalogRead", () => ({
  useCatalogItemDetail: (...args: unknown[]) => mocks.useCatalogItemDetail(...args),
}));

vi.mock("@/components/EditMetadataDialog", () => ({
  default: ({ item }: { item: ItemDetail }) => {
    mocks.editItem(item);
    return <div>Edit {item.title}</div>;
  },
}));

vi.mock("@/components/MatchItemDialog", () => ({
  default: ({ item }: { item: ItemDetail }) => {
    mocks.matchItem(item);
    return <div>Match {item.title}</div>;
  },
}));

vi.mock("@/hooks/queries/favorites", () => ({
  useToggleFavorite: () => ({
    isPending: false,
    mutateAsync: (...args: unknown[]) => mocks.toggleFavorite(...args),
  }),
}));

vi.mock("@/hooks/queries/watchlist", () => ({
  useToggleWatchlist: () => ({ isPending: false, mutateAsync: vi.fn() }),
}));

vi.mock("@/hooks/queries/notInterested", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/queries/notInterested")>()),
  useToggleNotInterested: () => ({ isPending: false, mutate: vi.fn(), mutateAsync: vi.fn() }),
}));

vi.mock("@/hooks/queries/items", () => ({
  useRefreshItemMetadata: () => ({ isPending: false, mutate: vi.fn() }),
  useWatchedStateMutation: () => ({
    isPending: false,
    mutateAsync: (...args: unknown[]) => mocks.toggleWatched(...args),
  }),
}));

vi.mock("@/hooks/useUICustomization", () => ({
  useUICustomization: () => ({ cardPresentation: { poster_size: mocks.posterSize } }),
}));

vi.mock("@/hooks/queries/homeDismissals", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/queries/homeDismissals")>()),
  useDismissHomeItem: () => ({ isPending: false, mutateAsync: vi.fn() }),
}));

vi.mock("@/hooks/useAuth", () => ({
  useOptionalAuth: () => mocks.authState,
}));

vi.mock("@/hooks/useCurrentProfile", () => ({
  useCurrentProfile: () => ({ profile: null, hasSelectedProfile: false }),
}));

vi.mock("@/hooks/useViewTransition", () => ({
  useViewTransitionNavigate: () => vi.fn(),
}));

vi.mock("@/playback/watchPlaybackContext", () => ({
  useWatchPlaybackController: () => ({ startPlayback: vi.fn() }),
}));

vi.mock("@/components/RefreshMetadataDialog", () => ({
  default: () => null,
}));

// A movie poster menu with every quick action; `userState` is a new object on each call.
function menu(props: Partial<ComponentProps<typeof MediaItemMenu>> = {}) {
  return (
    <MemoryRouter>
      <MediaItemMenu
        contentId="movie-1"
        mediaType="movie"
        userState={{ played: false, is_favorite: false, in_watchlist: false }}
        variant="poster"
        quickActionMode="both"
        {...props}
      />
    </MemoryRouter>
  );
}

beforeEach(() => {
  mocks.useCatalogItemDetail.mockReset();
  mocks.editItem.mockReset();
  mocks.matchItem.mockReset();
  mocks.toggleFavorite.mockReset();
  mocks.toggleFavorite.mockResolvedValue(undefined);
  mocks.toggleWatched.mockReset();
  mocks.toggleWatched.mockResolvedValue(undefined);
  mocks.posterSize = "standard";
  mocks.authState = undefined;
});

describe("buildMediaItemMenuModel", () => {
  it("returns watched/favorite/watchlist removal labels for active state", () => {
    const model = buildMediaItemMenuModel({
      mediaType: "movie",
      userState: {
        played: true,
        is_favorite: true,
        in_watchlist: true,
      },
      isAdmin: true,
    });
    const actions = model.filter((item) => item.kind === "action");

    expect(actions[0]?.label).toBe("Play from Beginning");
    expect(actions[1]?.label).toBe("Mark Unwatched");
    expect(actions[2]?.label).toBe("Remove from Favorites");
    expect(actions[3]?.label).toBe("Remove from Watchlist");
    expect(actions[4]?.label).toBe("Not Interested");
    expect(actions[5]?.label).toBe("View Play History");
    expect(actions[6]?.label).toBe("Refresh Metadata");
    expect(actions[7]?.label).toBe("Edit Metadata");
    expect(actions[8]?.label).toBe("Match Item");
    expect(model.some((item) => item.kind === "action" && item.label === "View Play History")).toBe(
      true,
    );
    expect(model.some((item) => item.kind === "action" && item.label === "Refresh Metadata")).toBe(
      true,
    );
  });

  it("offers Not Interested on movies and series only, and its undo once marked", () => {
    const labels = (
      mediaType: "movie" | "series" | "episode" | "audiobook",
      notInterested = false,
    ) =>
      buildMediaItemMenuModel({
        mediaType,
        userState: {
          played: false,
          is_favorite: false,
          in_watchlist: false,
          not_interested: notInterested,
        },
        isAdmin: false,
      })
        .filter((item) => item.kind === "action")
        .map((item) => item.label);

    expect(labels("movie")).toContain("Not Interested");
    expect(labels("series", true)).toContain("Show in Recommendations");
    expect(labels("episode")).not.toContain("Not Interested");
    expect(labels("audiobook")).not.toContain("Not Interested");
  });

  it("omits favorites and watchlist when showCollectionActions is false", () => {
    const model = buildMediaItemMenuModel({
      mediaType: "movie",
      userState: {
        played: false,
        is_favorite: false,
        in_watchlist: false,
      },
      isAdmin: false,
      showCollectionActions: false,
    });
    const actions = model.filter((item) => item.kind === "action");

    expect(actions).toHaveLength(1);
    expect(actions[0]?.label).toBe("Mark Watched");
  });

  it.each(["episode", "season"] as const)(
    "does not offer adding %s items to favorites or the watchlist",
    (mediaType) => {
      const model = buildMediaItemMenuModel({
        mediaType,
        userState: { played: false, is_favorite: false, in_watchlist: false },
        isAdmin: false,
      });
      const keys = model.flatMap((item) => (item.kind === "action" ? [item.key] : []));

      expect(keys).toEqual(["toggleWatched"]);
    },
  );

  it("keeps Remove for an episode that is already on a list", () => {
    const labelsFor = (userState: MediaItemUserState) =>
      buildMediaItemMenuModel({ mediaType: "episode", userState, isAdmin: false }).flatMap(
        (item) => (item.kind === "action" ? [item.label] : []),
      );

    expect(labelsFor({ played: false, is_favorite: true, in_watchlist: false })).toEqual([
      "Mark Watched",
      "Remove from Favorites",
    ]);
    expect(labelsFor({ played: false, is_favorite: false, in_watchlist: true })).toEqual([
      "Mark Watched",
      "Remove from Watchlist",
    ]);
  });

  it("omits admin actions for non-admin users", () => {
    const model = buildMediaItemMenuModel({
      mediaType: "movie",
      userState: {
        played: false,
        is_favorite: false,
        in_watchlist: false,
      },
      isAdmin: false,
    });
    const actions = model.filter((item) => item.kind === "action");

    expect(actions[0]?.label).toBe("Mark Watched");
    expect(actions[1]?.label).toBe("Add to Favorites");
    expect(actions[2]?.label).toBe("Add to Watchlist");
    expect(model.some((item) => item.kind === "action" && item.label === "View Play History")).toBe(
      false,
    );
    expect(model.some((item) => item.kind === "action" && item.label === "Refresh Metadata")).toBe(
      false,
    );
  });

  it("shows metadata actions to a metadata curator without exposing play history", () => {
    const model = buildMediaItemMenuModel({
      mediaType: "series",
      isAdmin: false,
      canCurateMetadata: true,
    });
    const labels = model.filter((item) => item.kind === "action").map((item) => item.label);

    expect(labels).toEqual(["Refresh Metadata", "Edit Metadata", "Match Item"]);
    expect(labels).not.toContain("View Play History");
  });

  it("limits edit and match card actions to movies and series", () => {
    const model = buildMediaItemMenuModel({
      mediaType: "episode",
      isAdmin: true,
    });
    const labels = model.filter((item) => item.kind === "action").map((item) => item.label);

    expect(labels).toContain("Refresh Metadata");
    expect(labels).not.toContain("Edit Metadata");
    expect(labels).not.toContain("Match Item");
  });

  it("offers Mark Watched and Mark Unwatched for a partly watched movie or episode", () => {
    // A rewatch stays watched, but its resume position makes it in progress.
    for (const played of [false, true]) {
      for (const mediaType of ["movie", "episode"] as const) {
        const model = buildMediaItemMenuModel({
          mediaType,
          hasPartialProgress: true,
          userState: { played, is_favorite: false, in_watchlist: false },
          isAdmin: false,
          showCollectionActions: false,
          dismissLabel: "Remove from Continue Watching",
        });

        expect(
          model.flatMap((item) => (item.kind === "action" ? [`${item.key}:${item.label}`] : [])),
          `${mediaType} played=${played}`,
        ).toEqual([
          "playFromBeginning:Play from Beginning",
          "markWatched:Mark Watched",
          "markUnwatched:Mark Unwatched",
          "dismissFromHome:Remove from Continue Watching",
        ]);
      }
    }
  });

  it("treats a rewatched episode as in progress in a list that hides its progress bar", () => {
    const model = buildMediaItemMenuModel({
      mediaType: "episode",
      hasPartialProgress: false,
      hasResumePosition: true,
      userState: { played: true, is_favorite: false, in_watchlist: false },
      isAdmin: false,
      showCollectionActions: false,
    });

    const keys = model.flatMap((item) => (item.kind === "action" ? [item.key] : []));
    expect(keys).toContain("markUnwatched");
    expect(keys).not.toContain("toggleWatched");
  });

  it("keeps the watched toggle when a movie or episode has no resume position", () => {
    const cases = [
      { mediaType: "movie", hasPartialProgress: false, played: true },
      { mediaType: "episode", hasPartialProgress: false, played: false },
      { mediaType: "audiobook", hasPartialProgress: true, played: false },
      { mediaType: "series", hasPartialProgress: true, played: false },
    ] as const;
    for (const { mediaType, hasPartialProgress, played } of cases) {
      const keys = buildMediaItemMenuModel({
        mediaType,
        hasPartialProgress,
        userState: { played, is_favorite: false, in_watchlist: false },
        isAdmin: false,
      }).flatMap((item) => (item.kind === "action" ? [item.key] : []));

      expect(keys, mediaType).toContain("toggleWatched");
      expect(keys, mediaType).not.toContain("markWatched");
      expect(keys, mediaType).not.toContain("markUnwatched");
    }
  });

  it("does not show play from beginning for non-leaf items", () => {
    const model = buildMediaItemMenuModel({
      mediaType: "series",
      userState: {
        played: true,
        is_favorite: false,
        in_watchlist: false,
      },
      isAdmin: false,
    });

    expect(
      model.some((item) => item.kind === "action" && item.label === "Play from Beginning"),
    ).toBe(false);
  });

  it("uses reading labels for ebook state actions", () => {
    const model = buildMediaItemMenuModel({
      mediaType: "ebook",
      userState: {
        played: false,
        is_favorite: false,
        in_watchlist: false,
      },
      isAdmin: false,
      dismissLabel: "Remove from Continue Reading",
    });
    const labels = model.filter((item) => item.kind === "action").map((item) => item.label);

    expect(labels).toEqual([
      "Mark Read",
      "Add to Favorites",
      "Add to Watchlist",
      "Remove from Continue Reading",
    ]);
    expect(labels).not.toContain("Mark Watched");
  });
});

describe("MediaItemMenu metadata dialogs", () => {
  const detail = {
    content_id: "series-1",
    type: "series",
    title: "Silo",
    year: 2023,
  } as ItemDetail;

  it("passes the full item and library context to Match Item", async () => {
    mocks.useCatalogItemDetail.mockReturnValue({ data: detail });

    render(
      <MetadataActionDialogHost
        action="match"
        contentId="series-1"
        libraryId={12}
        onClose={() => undefined}
      />,
    );

    expect(await screen.findByText("Match Silo")).toBeInTheDocument();
    expect(mocks.useCatalogItemDetail).toHaveBeenCalledWith("series-1", 12);
    expect(mocks.matchItem).toHaveBeenCalledWith({ ...detail, library_id: 12 });
  });
});

describe("MediaItemMenu trigger visibility", () => {
  it("returns focus to the trigger when a keyboard user closes the menu", async () => {
    render(menu());

    const trigger = screen.getByRole("button", { name: "More actions" });
    trigger.focus();
    await userEvent.keyboard("{Enter}");
    expect(screen.getByRole("menu")).toBeTruthy();

    await userEvent.keyboard("{Escape}");

    await waitFor(() => {
      expect(document.activeElement).toBe(trigger);
    });
  });

  function renderTriggerMenu() {
    render(menu());
    return screen.getByRole("button", { name: "More actions" });
  }

  it("opens on a lone click from assistive technology before the menu mounts", async () => {
    const trigger = renderTriggerMenu();
    expect(screen.queryByRole("menu")).toBeNull();

    // The trigger's wrapper cancels the click so the card link never sees it.
    expect(fireEvent.click(trigger)).toBe(false);

    expect(await screen.findByRole("menu")).toBeTruthy();
    expect(trigger.getAttribute("aria-expanded")).toBe("true");
  });

  it("opens once for a pointer press followed by its click", async () => {
    const trigger = renderTriggerMenu();

    fireEvent.pointerDown(trigger, { pointerId: 4, button: 0, pointerType: "mouse" });
    fireEvent.click(trigger, { detail: 1 });
    expect(await screen.findByRole("menu")).toBeTruthy();
    expect(trigger.getAttribute("data-state")).toBe("open");

    // Some assistive technology pairs a pointerdown with a detail-0 click.
    fireEvent.pointerDown(trigger, { pointerId: 5, button: 0, pointerType: "mouse" });
    fireEvent.click(trigger);
    await waitFor(() => expect(trigger.getAttribute("data-state")).toBe("closed"));
  });

  it("opens once for Space and Enter", async () => {
    const trigger = renderTriggerMenu();

    trigger.focus();
    await userEvent.keyboard(" ");
    expect(await screen.findByRole("menu")).toBeTruthy();
    expect(trigger.getAttribute("data-state")).toBe("open");

    await userEvent.keyboard("{Escape}");
    await waitFor(() => expect(document.activeElement).toBe(trigger));

    await userEvent.keyboard("{Enter}");
    expect(await screen.findByRole("menu")).toBeTruthy();
    expect(trigger.getAttribute("data-state")).toBe("open");
  });

  it("still returns keyboard focus after a cancelled pointer press inside the menu", async () => {
    render(menu());

    const trigger = screen.getByRole("button", { name: "More actions" });
    trigger.focus();
    await userEvent.keyboard("{Enter}");
    const menuItem = screen.getByRole("menuitem", { name: "Mark Watched" });

    fireEvent.pointerDown(menuItem, { pointerId: 3, button: 0 });
    await userEvent.keyboard("{Escape}");

    await waitFor(() => {
      expect(document.activeElement).toBe(trigger);
    });
  });

  it("optimistically marks watched, syncs the menu, and reverses the state", async () => {
    let resolveWatched!: () => void;
    mocks.toggleWatched.mockReturnValueOnce(
      new Promise<void>((resolve) => {
        resolveWatched = resolve;
      }),
    );
    render(menu());

    fireEvent.click(screen.getByRole("button", { name: "Mark Watched" }));

    expect(mocks.toggleWatched).toHaveBeenCalledWith(true);
    const watchedShortcut = await screen.findByRole("button", { name: "Mark Unwatched" });
    expect(watchedShortcut).toHaveAttribute("aria-pressed", "true");
    expect(watchedShortcut.className).toContain("text-emerald-400");
    expect(watchedShortcut.querySelector(".lucide-eye")).toBeTruthy();
    expect(screen.getByTestId("watched-burst")).toBeTruthy();

    resolveWatched();
    await userEvent.click(screen.getByRole("button", { name: "More actions" }));
    expect(
      screen.getByRole("menuitem", { name: "Mark Unwatched" }).querySelector(".lucide-eye"),
    ).toHaveClass("text-emerald-400");
    await userEvent.click(screen.getByRole("menuitem", { name: "Mark Unwatched" }));

    expect(mocks.toggleWatched).toHaveBeenLastCalledWith(false);
    await waitFor(() => {
      expect(screen.getByRole("button", { name: "Mark Watched" })).toHaveAttribute(
        "aria-pressed",
        "false",
      );
    });
  });

  it("unmarks a rewatch from its Mark Unwatched", async () => {
    render(
      menu({
        variant: "wide",
        hasPartialProgress: true,
        userState: { played: true, is_favorite: false, in_watchlist: false },
        quickActionMode: undefined,
      }),
    );

    await userEvent.click(screen.getByRole("button", { name: "More actions" }));
    await userEvent.click(screen.getByRole("menuitem", { name: "Mark Unwatched" }));

    expect(mocks.toggleWatched).toHaveBeenCalledWith(false);
    expect(mocks.toggleWatched).not.toHaveBeenCalledWith(true);
  });

  it("marks a rewatch watched from its card shortcut instead of unmarking it", async () => {
    render(
      menu({
        variant: "wide",
        hasPartialProgress: true,
        showWatchedShortcut: true,
        userState: { played: true, is_favorite: false, in_watchlist: false },
      }),
    );

    fireEvent.click(screen.getByRole("button", { name: "Mark Watched" }));

    await waitFor(() => expect(mocks.toggleWatched).toHaveBeenCalledWith(true));
    expect(mocks.toggleWatched).not.toHaveBeenCalledWith(false);
  });

  it("rolls the watched eye back when the request fails", async () => {
    mocks.toggleWatched.mockRejectedValueOnce(new Error("request failed"));
    render(menu());

    fireEvent.click(screen.getByRole("button", { name: "Mark Watched" }));

    await waitFor(() => {
      const shortcut = screen.getByRole("button", { name: "Mark Watched" });
      expect(shortcut).toHaveAttribute("aria-pressed", "false");
      expect(shortcut.querySelector(".lucide-eye-off")).toBeTruthy();
    });
  });

  it("uses the swipe-safe pointer path for the watched shortcut", async () => {
    render(menu());

    const shortcut = screen.getByRole("button", { name: "Mark Watched" });
    fireEvent.pointerDown(shortcut, { pointerId: 12, button: 0, clientX: 20, clientY: 30 });
    fireEvent.pointerMove(shortcut, { pointerId: 12, clientX: 44, clientY: 30 });
    fireEvent.pointerUp(shortcut, { pointerId: 12, button: 0, clientX: 24, clientY: 30 });
    expect(mocks.toggleWatched).not.toHaveBeenCalled();

    fireEvent.pointerDown(shortcut, { pointerId: 13, button: 0, clientX: 20, clientY: 30 });
    fireEvent.pointerUp(shortcut, { pointerId: 13, button: 0, clientX: 25, clientY: 35 });
    expect(mocks.toggleWatched).toHaveBeenCalledWith(true);
    await screen.findByRole("button", { name: "Mark Unwatched" });
  });

  it("limits automatic poster eyes to movies and series", () => {
    const episode = { contentId: "episode-1", mediaType: "episode" } as const;
    const { rerender } = render(menu(episode));

    expect(screen.queryByRole("button", { name: "Mark Watched" })).toBeNull();

    rerender(menu({ ...episode, showWatchedShortcut: true }));

    expect(screen.getByRole("button", { name: "Mark Watched" })).toBeTruthy();
  });

  it("shows only the quick actions selected by the resolved profile mode", () => {
    const userState = { played: false, is_favorite: false, in_watchlist: false };
    const renderMenu = (quickActionMode: "favorites" | "watched" | "none") =>
      menu({ userState, quickActionMode });
    const { rerender } = render(renderMenu("favorites"));

    expect(screen.getByRole("button", { name: "Add to favorites" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Mark Watched" })).toBeNull();

    rerender(renderMenu("watched"));
    expect(screen.queryByRole("button", { name: "Add to favorites" })).toBeNull();
    expect(screen.getByRole("button", { name: "Mark Watched" })).toBeTruthy();

    rerender(renderMenu("none"));
    expect(screen.queryByRole("button", { name: "Add to favorites" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Mark Watched" })).toBeNull();
    expect(screen.getByRole("button", { name: "More actions" })).toBeTruthy();
  });

  it("toggles through the shared favorite mutation and updates the heart immediately", async () => {
    render(menu());

    const button = screen.getByRole("button", { name: "Add to favorites" });
    expect(button.getAttribute("aria-pressed")).toBe("false");
    expect(button.className).toContain("media-card-action-trigger");
    expect(button.className).toContain("cursor-pointer");
    expect(button.className).not.toContain("cursor-wait");
    expect(button.parentElement?.className).toContain("left-2.5");
    await userEvent.click(button);

    expect(mocks.toggleFavorite).toHaveBeenCalledTimes(1);
    expect(mocks.toggleFavorite).toHaveBeenCalledWith(false);
    expect(screen.getByTestId("favorite-burst")).toBeTruthy();
    await waitFor(() => {
      const button = screen.getByRole("button", { name: "Remove from favorites" });
      expect(button.getAttribute("aria-pressed")).toBe("true");
      expect(button.querySelector("svg")?.getAttribute("class")).toContain("fill-red-500");
    });
    await userEvent.click(screen.getByRole("button", { name: "More actions" }));
    expect(screen.getByRole("menuitem", { name: "Remove from Favorites" })).toBeTruthy();
  });

  it("toggles on a short pointer release even when a carousel consumes the click", async () => {
    render(menu());

    const button = screen.getByRole("button", { name: "Add to favorites" });
    fireEvent.pointerDown(button, { pointerId: 9, button: 0, clientX: 120, clientY: 240 });
    fireEvent.pointerMove(button, { pointerId: 9, clientX: 144, clientY: 240 });
    fireEvent.pointerUp(button, { pointerId: 9, button: 0, clientX: 124, clientY: 240 });
    expect(mocks.toggleFavorite).not.toHaveBeenCalled();
    expect(screen.getByRole("button", { name: "Add to favorites" })).toBeTruthy();
    fireEvent.pointerDown(button, { pointerId: 7, button: 0, clientX: 120, clientY: 240 });
    fireEvent.pointerUp(button, { pointerId: 7, button: 0, clientX: 128, clientY: 248 });

    expect(mocks.toggleFavorite).toHaveBeenCalledTimes(1);
    expect(mocks.toggleFavorite).toHaveBeenCalledWith(false);
    await waitFor(() => {
      expect(screen.getByRole("button", { name: "Remove from favorites" })).toBeTruthy();
    });
  });

  it("keeps watched and favorite actions working for an unpaired mouse click", async () => {
    render(menu());

    fireEvent.click(screen.getByRole("button", { name: "Mark Watched" }), { detail: 1 });
    fireEvent.click(screen.getByRole("button", { name: "Add to favorites" }), { detail: 1 });

    expect(mocks.toggleWatched).toHaveBeenCalledWith(true);
    expect(mocks.toggleFavorite).toHaveBeenCalledWith(false);
    await waitFor(() => {
      expect(screen.getByRole("button", { name: "Mark Unwatched" })).toBeTruthy();
      expect(screen.getByRole("button", { name: "Remove from favorites" })).toBeTruthy();
    });
  });

  it("preserves an optimistic toggle when the parent rebuilds an equivalent user state", async () => {
    let resolveToggle: (() => void) | undefined;
    mocks.toggleWatched.mockReturnValueOnce(
      new Promise<void>((resolve) => {
        resolveToggle = resolve;
      }),
    );
    // menu() builds a new, equivalent userState object on every call.
    const renderMenu = () =>
      menu({
        contentId: "episode-1",
        mediaType: "episode",
        variant: "wide",
        showCollectionActions: false,
        showWatchedShortcut: true,
      });
    const { rerender } = render(renderMenu());

    fireEvent.click(screen.getByRole("button", { name: "Mark Watched" }));
    expect(screen.getByRole("button", { name: "Mark Unwatched" })).toBeTruthy();

    rerender(renderMenu());
    expect(screen.getByRole("button", { name: "Mark Unwatched" })).toBeTruthy();

    resolveToggle?.();
    await waitFor(() => expect(mocks.toggleWatched).toHaveBeenCalledWith(true));
  });

  it("does not toggle when a captured pointer is released outside the button", () => {
    render(menu());

    const button = screen.getByRole("button", { name: "Add to favorites" });
    vi.spyOn(button, "getBoundingClientRect").mockReturnValue({
      bottom: 250,
      height: 20,
      left: 110,
      right: 124,
      top: 230,
      width: 14,
      x: 110,
      y: 230,
      toJSON: () => ({}),
    });

    fireEvent.pointerDown(button, { pointerId: 8, button: 0, clientX: 120, clientY: 240 });
    fireEvent.pointerUp(button, { pointerId: 8, button: 0, clientX: 128, clientY: 240 });

    expect(mocks.toggleFavorite).not.toHaveBeenCalled();
  });

  it("keeps the poster heart in sync when favorite state changes through the menu", async () => {
    render(menu());

    await userEvent.click(screen.getByRole("button", { name: "More actions" }));
    await userEvent.click(screen.getByRole("menuitem", { name: "Add to Favorites" }));

    expect(mocks.toggleFavorite).toHaveBeenCalledWith(false);
    await waitFor(() => {
      expect(screen.getByRole("button", { name: "Remove from favorites" })).toBeTruthy();
    });
  });

  it("rolls the heart back when the favorite request fails", async () => {
    mocks.toggleFavorite.mockRejectedValueOnce(new Error("request failed"));
    render(menu());

    fireEvent.click(screen.getByRole("button", { name: "Add to favorites" }));

    await waitFor(() => {
      expect(screen.getByRole("button", { name: "Add to favorites" })).toBeTruthy();
    });
  });

  it("keeps the favorite shortcut off wide cards and collection-disabled menus", () => {
    const { rerender } = render(menu({ variant: "wide" }));

    expect(screen.queryByRole("button", { name: "Add to favorites" })).toBeNull();

    rerender(menu({ showCollectionActions: false }));

    expect(screen.queryByRole("button", { name: "Add to favorites" })).toBeNull();
  });
});

describe("MediaItemMenu long-press action sheet", () => {
  function LongPressCard({ onCardClick }: { onCardClick?: () => void }) {
    const cardRef = useRef<HTMLDivElement>(null);

    return (
      <MemoryRouter>
        <div ref={cardRef} data-testid="card">
          <a href="/item/movie-1" onClick={onCardClick}>
            Card link
          </a>
          <MediaItemMenu
            contentId="movie-1"
            mediaType="movie"
            userState={{ played: false, is_favorite: false, in_watchlist: false }}
            variant="poster"
            longPressRef={cardRef}
            itemTitle="Apex"
            quickActionMode="both"
          />
        </div>
      </MemoryRouter>
    );
  }

  function pressCard(clientX = 40, clientY = 60) {
    fireEvent.pointerDown(screen.getByTestId("card"), {
      pointerId: 1,
      pointerType: "touch",
      clientX,
      clientY,
    });
  }

  function holdPastLongPress() {
    act(() => {
      vi.advanceTimersByTime(600);
    });
  }

  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
    document.documentElement.removeAttribute("data-fine-pointer");
  });

  it("ignores mouse presses so precise pointers keep the hover controls", () => {
    render(<LongPressCard />);

    fireEvent.pointerDown(screen.getByTestId("card"), {
      pointerId: 2,
      pointerType: "mouse",
      clientX: 40,
      clientY: 60,
    });
    holdPastLongPress();

    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("cancels the hold once the pointer moves like a carousel swipe", () => {
    render(<LongPressCard />);

    pressCard();
    fireEvent.pointerMove(window, { pointerId: 1, pointerType: "touch", clientX: 96, clientY: 60 });
    holdPastLongPress();

    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("cancels the hold when the finger lifts before the delay", () => {
    render(<LongPressCard />);

    pressCard();
    act(() => {
      vi.advanceTimersByTime(200);
    });
    fireEvent.pointerUp(window, { pointerId: 1, pointerType: "touch", clientX: 40, clientY: 60 });
    holdPastLongPress();

    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("swallows the click the platform sends after the hold", () => {
    const onCardClick = vi.fn();
    render(<LongPressCard onCardClick={onCardClick} />);

    pressCard();
    holdPastLongPress();
    fireEvent.pointerUp(window, { pointerId: 1, pointerType: "touch", clientX: 40, clientY: 60 });

    const link = screen.getByText("Card link");
    expect(fireEvent.click(link)).toBe(false);
    expect(onCardClick).not.toHaveBeenCalled();
  });

  it("runs the selected action and closes the sheet", () => {
    render(<LongPressCard />);

    pressCard();
    holdPastLongPress();
    const sheet = screen.getByRole("dialog");
    expect(within(sheet).getByText("Apex")).toBeTruthy();
    expect(within(sheet).getByRole("button", { name: "Mark Watched" })).toBeTruthy();
    expect(within(sheet).getByRole("button", { name: "Add to Favorites" })).toBeTruthy();
    expect(within(sheet).getByRole("button", { name: "Add to Watchlist" })).toBeTruthy();
    fireEvent.click(within(sheet).getByRole("button", { name: "Mark Watched" }));

    expect(mocks.toggleWatched).toHaveBeenCalledWith(true);
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("keeps long press while omitting desktop-only shortcut trees on coarse-pointer devices", () => {
    // A coarse-pointer device is now declared by the published capability
    // rather than by the media query: the component follows the pointer that
    // was actually observed, because the query is unreliable on hybrids.
    document.documentElement.setAttribute("data-fine-pointer", "false");

    render(<LongPressCard />);

    expect(screen.queryByRole("button", { name: "Mark Watched" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Add to favorites" })).toBeNull();
    expect(screen.getByRole("button", { name: "More actions" })).toBeTruthy();

    pressCard();
    holdPastLongPress();

    expect(
      within(screen.getByRole("dialog")).getByRole("button", { name: "Mark Watched" }),
    ).toBeTruthy();
  });
});

describe("MediaItemMenu lazy machinery", () => {
  const radixRoot = () => document.querySelector('[data-slot="dropdown-menu-trigger"]');

  it("mounts no menu root until the card is used, then opens on the first press", async () => {
    render(menu());
    const trigger = screen.getByRole("button", { name: "More actions" });
    expect(radixRoot()).toBeNull();
    expect(trigger).toHaveAttribute("aria-expanded", "false");
    expect(trigger).toHaveAttribute("data-state", "closed");

    await userEvent.click(trigger);

    expect(screen.getByRole("menu")).toBeTruthy();
    expect(radixRoot()).not.toBeNull();
    // The visible trigger kept its element and reports the open menu.
    expect(screen.getByRole("button", { name: "More actions" })).toBe(trigger);
    expect(trigger).toHaveAttribute("aria-expanded", "true");
    expect(trigger).toHaveAttribute("data-state", "open");
    expect(trigger.getAttribute("aria-controls")).toBe(screen.getByRole("menu").id);
  });

  it("mounts the menu when the trigger takes focus and opens to the first item by keyboard", async () => {
    render(menu());
    const trigger = screen.getByRole("button", { name: "More actions" });

    act(() => trigger.focus());
    expect(radixRoot()).not.toBeNull();
    expect(document.activeElement).toBe(trigger);

    await userEvent.keyboard("{Enter}");

    const items = screen.getAllByRole("menuitem");
    await waitFor(() => expect(document.activeElement).toBe(items[0]));
  });

  it("runs a quick action pressed before the machinery mounted", () => {
    render(menu());

    fireEvent.click(screen.getByRole("button", { name: "Add to favorites" }));

    expect(mocks.toggleFavorite).toHaveBeenCalledWith(false);
    expect(screen.getByRole("button", { name: "Remove from favorites" })).toBeTruthy();
  });
});
