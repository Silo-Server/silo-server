import { renderToStaticMarkup } from "react-dom/server";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import type { ItemDetail } from "@/api/types";
import { beforeEach, describe, expect, it, vi } from "vitest";
import MediaItemMenu, { buildMediaItemMenuModel, MetadataActionDialogHost } from "./MediaItemMenu";
import { mediaItemMenuTriggerClassName } from "./mediaItemMenuTrigger";

const mocks = vi.hoisted(() => ({
  useCatalogItemDetail: vi.fn(),
  editItem: vi.fn(),
  matchItem: vi.fn(),
  toggleFavorite: vi.fn(),
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
  useWatchedStateMutation: () => ({ isPending: false, mutateAsync: vi.fn() }),
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
  it("uses open state and keyboard focus without keeping a mouse-closed card focused", () => {
    const className = mediaItemMenuTriggerClassName();

    expect(className).toContain("pointer-fine:group-hover/card:opacity-100");
    expect(className).toContain("pointer-fine:data-[state=open]:opacity-100");
    expect(className).toContain("pointer-fine:focus-visible:opacity-100");
    expect(className).not.toContain("md:opacity-0");
    expect(className).not.toContain("group-focus-within");
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
    expect(button.className).toContain("pointer-fine:group-hover/card:opacity-100");
    expect(button.className).toContain("cursor-pointer");
    expect(button.className).not.toContain("cursor-wait");
    expect(button.parentElement?.className).toContain("left-2.5");
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
});
