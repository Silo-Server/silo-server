import { describe, expect, it } from "vitest";
import { buildMediaItemMenuModel } from "./MediaItemMenu";

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

    expect(actions).toHaveLength(4);
    expect(actions[0]?.label).toBe("Play from Beginning");
    expect(actions[1]?.label).toBe("Mark Unwatched");
    expect(actions[2]?.label).toBe("View Play History");
    expect(actions[3]?.label).toBe("Refresh Metadata");
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
});
