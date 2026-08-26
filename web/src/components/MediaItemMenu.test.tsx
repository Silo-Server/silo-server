import { act, useRef } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import type { ItemDetail } from "@/api/types";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import MediaItemMenu, { buildMediaItemMenuModel, MetadataActionDialogHost } from "./MediaItemMenu";
import { mediaItemMenuTriggerClassName } from "./mediaItemMenuTrigger";

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

vi.mock("@/hooks/queries/homeDismissals", () => ({
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
    expect(actions[4]?.label).toBe("View Play History");
    expect(actions[5]?.label).toBe("Refresh Metadata");
    expect(actions[6]?.label).toBe("Edit Metadata");
    expect(actions[7]?.label).toBe("Match Item");
    expect(model.some((item) => item.kind === "action" && item.label === "View Play History")).toBe(
      true,
    );
    expect(model.some((item) => item.kind === "action" && item.label === "Refresh Metadata")).toBe(
      true,
    );
  });

  it("omits favorites and watchlist when showCollectionActions is false", () => {
    const model = buildMediaItemMenuModel({
      mediaType: "episode",
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

  it("shows watched toggle and admin actions when showCollectionActions is false for admins", () => {
    const model = buildMediaItemMenuModel({
      mediaType: "movie",
      userState: {
        played: true,
        is_favorite: false,
        in_watchlist: false,
      },
      isAdmin: true,
      showCollectionActions: false,
    });
    const actions = model.filter((item) => item.kind === "action");

    expect(actions).toHaveLength(6);
    expect(actions[0]?.label).toBe("Play from Beginning");
    expect(actions[1]?.label).toBe("Mark Unwatched");
    expect(actions[2]?.label).toBe("View Play History");
    expect(actions[3]?.label).toBe("Refresh Metadata");
    expect(actions[4]?.label).toBe("Edit Metadata");
    expect(actions[5]?.label).toBe("Match Item");
  });

  it("omits admin actions for non-admin users", () => {
    const model = buildMediaItemMenuModel({
      mediaType: "episode",
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

  it("shows a continue watching dismissal action when provided", () => {
    const model = buildMediaItemMenuModel({
      mediaType: "episode",
      userState: {
        played: false,
        is_favorite: false,
        in_watchlist: false,
      },
      isAdmin: false,
      dismissLabel: "Remove from Continue Watching",
    });

    expect(
      model.some(
        (item) => item.kind === "action" && item.label === "Remove from Continue Watching",
      ),
    ).toBe(true);
  });

  it("shows a next up dismissal action when provided", () => {
    const model = buildMediaItemMenuModel({
      mediaType: "episode",
      userState: {
        played: false,
        is_favorite: false,
        in_watchlist: false,
      },
      isAdmin: false,
      dismissLabel: "Remove from Next Up",
    });

    expect(
      model.some((item) => item.kind === "action" && item.label === "Remove from Next Up"),
    ).toBe(true);
  });

  it("shows play from beginning for partially watched leaf items", () => {
    const model = buildMediaItemMenuModel({
      mediaType: "episode",
      hasPartialProgress: true,
      userState: {
        played: false,
        is_favorite: false,
        in_watchlist: false,
      },
      isAdmin: false,
      showCollectionActions: false,
    });

    expect(
      model.some((item) => item.kind === "action" && item.label === "Play from Beginning"),
    ).toBe(true);
  });

  it("uses listening labels for audiobook state actions", () => {
    const model = buildMediaItemMenuModel({
      mediaType: "audiobook",
      hasPartialProgress: true,
      userState: {
        played: false,
        is_favorite: false,
        in_watchlist: false,
      },
      isAdmin: false,
      dismissLabel: "Remove from Continue Listening",
    });
    const actions = model.filter((item) => item.kind === "action");

    expect(actions[0]?.label).toBe("Listen from Beginning");
    expect(actions[1]?.label).toBe("Mark Listened");
    expect(actions.some((item) => item.label === "Remove from Continue Listening")).toBe(true);
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

  it("uses the unread label for ebooks already marked read", () => {
    const model = buildMediaItemMenuModel({
      mediaType: "ebook",
      userState: {
        played: true,
        is_favorite: false,
        in_watchlist: false,
      },
      isAdmin: false,
    });
    const labels = model.filter((item) => item.kind === "action").map((item) => item.label);

    expect(labels).toContain("Mark Unread");
  });
});

describe("MediaItemMenu metadata dialogs", () => {
  const detail = {
    content_id: "series-1",
    type: "series",
    title: "Silo",
    year: 2023,
  } as ItemDetail;

  it("loads the exact item detail and passes it to Edit Metadata", () => {
    mocks.useCatalogItemDetail.mockReturnValue({ data: detail });

    const markup = renderToStaticMarkup(
      <MetadataActionDialogHost
        action="edit"
        contentId="series-1"
        libraryId={12}
        onClose={() => undefined}
      />,
    );

    expect(mocks.useCatalogItemDetail).toHaveBeenCalledWith("series-1", 12);
    expect(mocks.editItem).toHaveBeenCalledWith(detail);
    expect(markup).toContain("Edit Silo");
  });

  it("passes the full item and library context to Match Item", () => {
    mocks.useCatalogItemDetail.mockReturnValue({ data: detail });

    const markup = renderToStaticMarkup(
      <MetadataActionDialogHost
        action="match"
        contentId="series-1"
        libraryId={12}
        onClose={() => undefined}
      />,
    );

    expect(mocks.useCatalogItemDetail).toHaveBeenCalledWith("series-1", 12);
    expect(mocks.matchItem).toHaveBeenCalledWith({ ...detail, library_id: 12 });
    expect(markup).toContain("Match Silo");
  });
});

describe("MediaItemMenu trigger visibility", () => {
  it("leaves reveal rules to the media-card CSS instead of utility classes", () => {
    const className = mediaItemMenuTriggerClassName();

    expect(className).toContain("media-card-action-trigger");
    expect(className).not.toContain("pointer-fine:");
    expect(className).not.toContain("opacity-");
    expect(className).not.toContain("group-hover");
    expect(className).not.toContain("group-focus-within");
    expect(className).toContain("focus-visible:ring-2");
    expect(className).toContain("size-6");
    expect(className).toContain("sm:size-8");
  });

  it("drops pointer focus when the trigger closes the menu so hover exit can hide it", async () => {
    render(
      <MemoryRouter>
        <MediaItemMenu
          contentId="movie-1"
          mediaType="movie"
          userState={{ played: false, is_favorite: false, in_watchlist: false }}
          variant="poster"
        />
      </MemoryRouter>,
    );

    const trigger = screen.getByRole("button", { name: "More actions" });
    await userEvent.click(trigger);
    expect(screen.getByRole("menu")).toBeTruthy();

    await userEvent.click(trigger);

    await waitFor(() => {
      expect(trigger.getAttribute("data-state")).toBe("closed");
      expect(document.activeElement).not.toBe(trigger);
    });
  });

  it("returns focus to the trigger when a keyboard user closes the menu", async () => {
    render(
      <MemoryRouter>
        <MediaItemMenu
          contentId="movie-1"
          mediaType="movie"
          userState={{ played: false, is_favorite: false, in_watchlist: false }}
          variant="poster"
        />
      </MemoryRouter>,
    );

    const trigger = screen.getByRole("button", { name: "More actions" });
    trigger.focus();
    await userEvent.keyboard("{Enter}");
    expect(screen.getByRole("menu")).toBeTruthy();

    await userEvent.keyboard("{Escape}");

    await waitFor(() => {
      expect(document.activeElement).toBe(trigger);
    });
  });

  it("still returns keyboard focus after a cancelled pointer press inside the menu", async () => {
    render(
      <MemoryRouter>
        <MediaItemMenu
          contentId="movie-1"
          mediaType="movie"
          userState={{ played: false, is_favorite: false, in_watchlist: false }}
          variant="poster"
        />
      </MemoryRouter>,
    );

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

  it("renders a matching bottom-left favorite control for poster cards", () => {
    render(
      <MemoryRouter>
        <MediaItemMenu
          contentId="movie-1"
          mediaType="movie"
          userState={{ played: false, is_favorite: false, in_watchlist: false }}
          variant="poster"
        />
      </MemoryRouter>,
    );

    const button = screen.getByRole("button", { name: "Add to favorites" });
    expect(button.getAttribute("aria-pressed")).toBe("false");
    expect(button.className).toContain("media-card-action-trigger");
    expect(button.className).toContain("cursor-pointer");
    expect(button.className).not.toContain("cursor-wait");
    expect(button.parentElement?.className).toContain("left-2.5");
  });

  it("shows matching eye state in the hover control and three-dot menu", async () => {
    render(
      <MemoryRouter>
        <MediaItemMenu
          contentId="movie-1"
          mediaType="movie"
          userState={{ played: false, is_favorite: false, in_watchlist: false }}
          variant="poster"
        />
      </MemoryRouter>,
    );

    const shortcut = screen.getByRole("button", { name: "Mark Watched" });
    expect(shortcut).toHaveAttribute("aria-pressed", "false");
    expect(shortcut.querySelector(".lucide-eye-off")).toBeTruthy();
    expect(shortcut.className).not.toContain("cursor-wait");
    expect(shortcut.parentElement?.className).toContain("z-20");

    await userEvent.click(screen.getByRole("button", { name: "More actions" }));
    expect(
      screen.getByRole("menuitem", { name: "Mark Watched" }).querySelector(".lucide-eye-off"),
    ).toBeTruthy();
  });

  it("optimistically marks watched, syncs the menu, and reverses the state", async () => {
    let resolveWatched!: () => void;
    mocks.toggleWatched.mockReturnValueOnce(
      new Promise<void>((resolve) => {
        resolveWatched = resolve;
      }),
    );
    render(
      <MemoryRouter>
        <MediaItemMenu
          contentId="movie-1"
          mediaType="movie"
          userState={{ played: false, is_favorite: false, in_watchlist: false }}
          variant="poster"
        />
      </MemoryRouter>,
    );

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

  it("rolls the watched eye back when the request fails", async () => {
    mocks.toggleWatched.mockRejectedValueOnce(new Error("request failed"));
    render(
      <MemoryRouter>
        <MediaItemMenu
          contentId="movie-1"
          mediaType="movie"
          userState={{ played: false, is_favorite: false, in_watchlist: false }}
          variant="poster"
        />
      </MemoryRouter>,
    );

    fireEvent.click(screen.getByRole("button", { name: "Mark Watched" }));

    await waitFor(() => {
      const shortcut = screen.getByRole("button", { name: "Mark Watched" });
      expect(shortcut).toHaveAttribute("aria-pressed", "false");
      expect(shortcut.querySelector(".lucide-eye-off")).toBeTruthy();
    });
  });

  it("uses the swipe-safe pointer path for the watched shortcut", async () => {
    render(
      <MemoryRouter>
        <MediaItemMenu
          contentId="movie-1"
          mediaType="movie"
          userState={{ played: false, is_favorite: false, in_watchlist: false }}
          variant="poster"
        />
      </MemoryRouter>,
    );

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

  it("shows the eye on opted-in wide cards and sizes narrow poster controls independently", () => {
    const { rerender } = render(
      <MemoryRouter>
        <MediaItemMenu
          contentId="movie-1"
          mediaType="movie"
          userState={{ played: false, is_favorite: false, in_watchlist: false }}
          variant="wide"
        />
      </MemoryRouter>,
    );

    expect(screen.queryByRole("button", { name: "Mark Watched" })).toBeNull();

    rerender(
      <MemoryRouter>
        <MediaItemMenu
          contentId="movie-1"
          mediaType="movie"
          userState={{ played: false, is_favorite: false, in_watchlist: false }}
          variant="wide"
          showWatchedShortcut
        />
      </MemoryRouter>,
    );
    expect(screen.getByRole("button", { name: "Mark Watched" }).className).toContain("size-9");

    rerender(
      <MemoryRouter>
        <MediaItemMenu
          contentId="movie-1"
          mediaType="movie"
          userState={{ played: false, is_favorite: false, in_watchlist: false }}
          variant="poster"
          narrowPosterActions
        />
      </MemoryRouter>,
    );

    for (const button of screen.getAllByRole("button")) {
      expect(button.className).toContain("size-6");
      expect(button.className).not.toContain("sm:size-7");
      expect(button.className).not.toContain("sm:size-8");
    }
    const quickActionClasses =
      screen.getByRole("button", { name: "Mark Watched" }).parentElement?.className.split(/\s+/) ??
      [];
    expect(quickActionClasses).toContain("left-1.5");
    expect(quickActionClasses).not.toContain("sm:left-2");

    mocks.posterSize = "compact";
    rerender(
      <MemoryRouter>
        <MediaItemMenu
          contentId="movie-1"
          mediaType="movie"
          userState={{ played: false, is_favorite: false, in_watchlist: false }}
          variant="poster"
        />
      </MemoryRouter>,
    );
    expect(screen.getByRole("button", { name: "Mark Watched" }).className).toContain("sm:size-7");
  });

  it("limits automatic poster eyes to movies and series", () => {
    const { rerender } = render(
      <MemoryRouter>
        <MediaItemMenu
          contentId="episode-1"
          mediaType="episode"
          userState={{ played: false, is_favorite: false, in_watchlist: false }}
          variant="poster"
        />
      </MemoryRouter>,
    );

    expect(screen.queryByRole("button", { name: "Mark Watched" })).toBeNull();

    rerender(
      <MemoryRouter>
        <MediaItemMenu
          contentId="episode-1"
          mediaType="episode"
          userState={{ played: false, is_favorite: false, in_watchlist: false }}
          variant="poster"
          showWatchedShortcut
        />
      </MemoryRouter>,
    );

    expect(screen.getByRole("button", { name: "Mark Watched" })).toBeTruthy();
  });

  it("uses matching action icons and sizes the menu to its longest entry", async () => {
    mocks.authState = { user: { role: "admin" } };
    render(
      <MemoryRouter>
        <MediaItemMenu
          contentId="movie-1"
          mediaType="movie"
          userState={{ played: true, is_favorite: true, in_watchlist: false }}
          variant="poster"
          dismissAction={{ itemId: "movie-1", surface: "continue_watching" }}
        />
      </MemoryRouter>,
    );

    await userEvent.click(screen.getByRole("button", { name: "More actions" }));

    const menu = screen.getByRole("menu");
    expect(menu.className).toContain("w-max");
    expect(menu.className).toContain("max-w-[calc(100vw-2rem)]");
    expect(menu.className).toContain("min-w-0");
    expect(menu.className).not.toContain("w-56");
    for (const item of screen.getAllByRole("menuitem")) {
      expect(item.querySelector("svg"), item.textContent ?? "menu item").toBeTruthy();
    }
    expect(
      screen
        .getByRole("menuitem", { name: "Remove from Favorites" })
        .querySelector(".lucide-heart"),
    ).toBeTruthy();
    expect(
      screen.getByRole("menuitem", { name: "Add to Watchlist" }).querySelector(".lucide-plus"),
    ).toBeTruthy();
    expect(
      screen.getByRole("menuitem", { name: "Edit Metadata" }).querySelector(".lucide-pencil"),
    ).toBeTruthy();
    expect(
      screen.getByRole("menuitem", { name: "Match Item" }).querySelector(".lucide-search"),
    ).toBeTruthy();
  });

  it("toggles through the shared favorite mutation and updates the heart immediately", async () => {
    render(
      <MemoryRouter>
        <MediaItemMenu
          contentId="movie-1"
          mediaType="movie"
          userState={{ played: false, is_favorite: false, in_watchlist: false }}
          variant="poster"
        />
      </MemoryRouter>,
    );

    await userEvent.click(screen.getByRole("button", { name: "Add to favorites" }));

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
    render(
      <MemoryRouter>
        <MediaItemMenu
          contentId="movie-1"
          mediaType="movie"
          userState={{ played: false, is_favorite: false, in_watchlist: false }}
          variant="poster"
        />
      </MemoryRouter>,
    );

    const button = screen.getByRole("button", { name: "Add to favorites" });
    fireEvent.pointerDown(button, { pointerId: 7, button: 0, clientX: 120, clientY: 240 });
    fireEvent.pointerUp(button, { pointerId: 7, button: 0, clientX: 128, clientY: 248 });

    expect(mocks.toggleFavorite).toHaveBeenCalledTimes(1);
    expect(mocks.toggleFavorite).toHaveBeenCalledWith(false);
    await waitFor(() => {
      expect(screen.getByRole("button", { name: "Remove from favorites" })).toBeTruthy();
    });
  });

  it("keeps watched and favorite actions working for an unpaired mouse click", async () => {
    render(
      <MemoryRouter>
        <MediaItemMenu
          contentId="movie-1"
          mediaType="movie"
          userState={{ played: false, is_favorite: false, in_watchlist: false }}
          variant="poster"
        />
      </MemoryRouter>,
    );

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
    const renderMenu = () => (
      <MemoryRouter>
        <MediaItemMenu
          contentId="episode-1"
          mediaType="episode"
          userState={{ played: false, is_favorite: false, in_watchlist: false }}
          variant="wide"
          showCollectionActions={false}
          showWatchedShortcut
        />
      </MemoryRouter>
    );
    const { rerender } = render(renderMenu());

    fireEvent.click(screen.getByRole("button", { name: "Mark Watched" }));
    expect(screen.getByRole("button", { name: "Mark Unwatched" })).toBeTruthy();

    rerender(renderMenu());
    expect(screen.getByRole("button", { name: "Mark Unwatched" })).toBeTruthy();

    resolveToggle?.();
    await waitFor(() => expect(mocks.toggleWatched).toHaveBeenCalledWith(true));
  });

  it("does not toggle when a captured pointer is released outside the button", () => {
    render(
      <MemoryRouter>
        <MediaItemMenu
          contentId="movie-1"
          mediaType="movie"
          userState={{ played: false, is_favorite: false, in_watchlist: false }}
          variant="poster"
        />
      </MemoryRouter>,
    );

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

  it("does not favorite when a swipe returns near its starting point", () => {
    render(
      <MemoryRouter>
        <MediaItemMenu
          contentId="movie-1"
          mediaType="movie"
          userState={{ played: false, is_favorite: false, in_watchlist: false }}
          variant="poster"
        />
      </MemoryRouter>,
    );

    const button = screen.getByRole("button", { name: "Add to favorites" });
    fireEvent.pointerDown(button, { pointerId: 9, button: 0, clientX: 120, clientY: 240 });
    fireEvent.pointerMove(button, { pointerId: 9, clientX: 144, clientY: 240 });
    fireEvent.pointerUp(button, { pointerId: 9, button: 0, clientX: 124, clientY: 240 });

    expect(mocks.toggleFavorite).not.toHaveBeenCalled();
    expect(screen.getByRole("button", { name: "Add to favorites" })).toBeTruthy();
  });

  it("keeps the poster heart in sync when favorite state changes through the menu", async () => {
    render(
      <MemoryRouter>
        <MediaItemMenu
          contentId="movie-1"
          mediaType="movie"
          userState={{ played: false, is_favorite: false, in_watchlist: false }}
          variant="poster"
        />
      </MemoryRouter>,
    );

    await userEvent.click(screen.getByRole("button", { name: "More actions" }));
    await userEvent.click(screen.getByRole("menuitem", { name: "Add to Favorites" }));

    expect(mocks.toggleFavorite).toHaveBeenCalledWith(false);
    await waitFor(() => {
      expect(screen.getByRole("button", { name: "Remove from favorites" })).toBeTruthy();
    });
  });

  it("unfavorites through the heart and updates the matching menu action", async () => {
    render(
      <MemoryRouter>
        <MediaItemMenu
          contentId="movie-1"
          mediaType="movie"
          userState={{ played: false, is_favorite: true, in_watchlist: false }}
          variant="poster"
        />
      </MemoryRouter>,
    );

    fireEvent.click(screen.getByRole("button", { name: "Remove from favorites" }));

    expect(mocks.toggleFavorite).toHaveBeenCalledWith(true);
    await waitFor(() => {
      expect(screen.getByRole("button", { name: "Add to favorites" })).toBeTruthy();
    });
    await userEvent.click(screen.getByRole("button", { name: "More actions" }));
    expect(screen.getByRole("menuitem", { name: "Add to Favorites" })).toBeTruthy();
  });

  it("unfavorites through the menu and clears the poster heart", async () => {
    render(
      <MemoryRouter>
        <MediaItemMenu
          contentId="movie-1"
          mediaType="movie"
          userState={{ played: false, is_favorite: true, in_watchlist: false }}
          variant="poster"
        />
      </MemoryRouter>,
    );

    await userEvent.click(screen.getByRole("button", { name: "More actions" }));
    await userEvent.click(screen.getByRole("menuitem", { name: "Remove from Favorites" }));

    expect(mocks.toggleFavorite).toHaveBeenCalledWith(true);
    await waitFor(() => {
      const button = screen.getByRole("button", { name: "Add to favorites" });
      expect(button.getAttribute("aria-pressed")).toBe("false");
      expect(button.querySelector("svg")?.getAttribute("class")).not.toContain("fill-red-500");
    });
  });

  it("rolls the heart back when the favorite request fails", async () => {
    mocks.toggleFavorite.mockRejectedValueOnce(new Error("request failed"));
    render(
      <MemoryRouter>
        <MediaItemMenu
          contentId="movie-1"
          mediaType="movie"
          userState={{ played: false, is_favorite: false, in_watchlist: false }}
          variant="poster"
        />
      </MemoryRouter>,
    );

    fireEvent.click(screen.getByRole("button", { name: "Add to favorites" }));

    await waitFor(() => {
      expect(screen.getByRole("button", { name: "Add to favorites" })).toBeTruthy();
    });
  });

  it("keeps the favorite shortcut off wide cards and collection-disabled menus", () => {
    const { rerender } = render(
      <MemoryRouter>
        <MediaItemMenu
          contentId="movie-1"
          mediaType="movie"
          userState={{ played: false, is_favorite: false, in_watchlist: false }}
          variant="wide"
        />
      </MemoryRouter>,
    );

    expect(screen.queryByRole("button", { name: "Add to favorites" })).toBeNull();

    rerender(
      <MemoryRouter>
        <MediaItemMenu
          contentId="movie-1"
          mediaType="movie"
          userState={{ played: false, is_favorite: false, in_watchlist: false }}
          variant="poster"
          showCollectionActions={false}
        />
      </MemoryRouter>,
    );

    expect(screen.queryByRole("button", { name: "Add to favorites" })).toBeNull();
  });

  it("can hide the poster heart without removing the favorite menu action", async () => {
    render(
      <MemoryRouter>
        <MediaItemMenu
          contentId="movie-1"
          mediaType="movie"
          userState={{ played: false, is_favorite: false, in_watchlist: false }}
          variant="poster"
          showFavoriteShortcut={false}
        />
      </MemoryRouter>,
    );

    expect(screen.queryByRole("button", { name: "Add to favorites" })).toBeNull();
    expect(screen.getByRole("button", { name: "Mark Watched" })).toBeTruthy();

    await userEvent.click(screen.getByRole("button", { name: "More actions" }));
    expect(screen.getByRole("menuitem", { name: "Add to Favorites" })).toBeTruthy();
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
  });

  it("opens the sheet with the full action set after a touch hold", () => {
    render(<LongPressCard />);

    pressCard();
    holdPastLongPress();

    const sheet = screen.getByRole("dialog");
    expect(within(sheet).getByText("Apex")).toBeTruthy();
    expect(within(sheet).getByRole("button", { name: "Mark Watched" })).toBeTruthy();
    expect(within(sheet).getByRole("button", { name: "Add to Favorites" })).toBeTruthy();
    expect(within(sheet).getByRole("button", { name: "Add to Watchlist" })).toBeTruthy();
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
    fireEvent.click(
      within(screen.getByRole("dialog")).getByRole("button", { name: "Mark Watched" }),
    );

    expect(mocks.toggleWatched).toHaveBeenCalledWith(true);
    expect(screen.queryByRole("dialog")).toBeNull();
  });
});
