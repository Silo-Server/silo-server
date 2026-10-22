import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import Notifications from "./Notifications";
const state = vi.hoisted(() => ({
  list: {
    data: undefined as unknown,
    isLoading: false,
    isError: false,
    hasNextPage: false,
    restart: vi.fn(),
  },
  all: { mutateAsync: vi.fn(), isPending: false, isError: false, reset: vi.fn() },
  capability: undefined as unknown,
  prefs: undefined as unknown,
  series: {} as Record<string, unknown>,
  seriesOptions: [] as unknown[],
  mute: { mutate: vi.fn(), isPending: false, variables: undefined as unknown },
}));
vi.mock("@/hooks/useAuth", () => ({ useAuth: () => ({}) }));
vi.mock("@/api/v2/notifications", () => ({ notificationScope: () => "owner" }));
vi.mock("@/hooks/useDocumentTitle", () => ({ useDocumentTitle: () => {} }));
vi.mock("@/hooks/queries/notificationWebhooks", () => ({
  useNotificationCapability: () => ({ data: state.capability }),
}));
vi.mock("@/hooks/queries/notifications", async (importOriginal) => ({
  formatEpisodeCode: (await importOriginal<typeof import("@/hooks/queries/notifications")>())
    .formatEpisodeCode,
  useNotificationSeries: (status: string, options: unknown) => {
    state.seriesOptions.push(options);
    return state.series[status];
  },
  useNotifications: () => state.list,
  useUnreadNotificationCount: () => ({ data: 2 }),
  useMarkAllNotificationsRead: () => state.all,
  useMarkNotificationRead: () => ({ mutate: vi.fn() }),
  useNotificationPreferences: () => ({ data: state.prefs, isLoading: false }),
  useUpdateNotificationPreferences: () => ({ mutate: vi.fn() }),
  useSetNotificationSeriesMuted: () => state.mute,
}));
const row = {
  id: "one",
  type: "episode.available",
  series_title: "Displayed episode",
  created_at: "2026-01-01T00:00:00Z",
  reason_flags: {},
  read_at: null,
};
function renderAt(path = "/") {
  render(
    <MemoryRouter initialEntries={[path]}>
      <Notifications />
    </MemoryRouter>,
  );
}
/** Lists one notification in an inbox whose read cutoff is "c". */
function showOnly(notification: Record<string, unknown>) {
  state.list.data = { pages: [{ notifications: [notification], read_cutoff: "c" }] };
}
beforeEach(() => {
  state.list.data = { pages: [{ notifications: [row], read_cutoff: "frozen-cutoff" }] };
  state.list.isError = false;
  state.all.isError = false;
  state.all.isPending = false;
  state.all.mutateAsync.mockReset();
  state.all.reset.mockReset();
  state.list.restart.mockReset().mockResolvedValue(undefined);
  state.capability = undefined;
  state.prefs = undefined;
  state.series = {};
  state.seriesOptions = [];
  state.mute.mutate.mockReset();
  state.mute.isPending = false;
  state.mute.variables = undefined;
});
afterEach(cleanup);
it("submits the displayed cutoff once while the mutation remains pending", async () => {
  let finish!: () => void;
  state.all.mutateAsync.mockReturnValue(
    new Promise<void>((resolve) => {
      finish = resolve;
    }),
  );
  renderAt();
  const button = screen.getByRole("button", { name: "Mark all read" });
  fireEvent.click(button);
  fireEvent.click(button);
  expect(state.all.mutateAsync).toHaveBeenCalledExactlyOnceWith("frozen-cutoff");
  expect(screen.getByText("Displayed episode")).toBeInTheDocument();
  finish();
});
it("retains partial rows and requires explicit reload after a mark-all failure", async () => {
  state.list.isError = true;
  state.all.isError = true;
  renderAt();
  expect(screen.getByText("Displayed episode")).toBeInTheDocument();
  expect(screen.queryByText("No notifications yet")).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Mark all read" })).toBeDisabled();
  fireEvent.click(screen.getByRole("button", { name: "Reload inbox" }));
  await waitFor(() => expect(state.all.reset).toHaveBeenCalledOnce());
  expect(state.list.restart).toHaveBeenCalledOnce();
  expect(state.all.mutateAsync).not.toHaveBeenCalled();
});
it.each([
  [
    "links a request.approved row to the title's page",
    {
      type: "request.approved",
      reason_flags: { media_type: "movie", tmdb_id: 603, title: "The Matrix" },
    },
    "/title/movie/603",
  ],
  [
    "keeps a fulfilled request linked to its catalog item",
    {
      type: "request.fulfilled",
      series_id: "movie-603",
      series_title: "The Matrix",
      reason_flags: { media_type: "movie", tmdb_id: 603 },
    },
    "/item/movie-603",
  ],
])("%s", (_title, notification, href) => {
  showOnly({ ...row, ...notification });
  renderAt();
  expect(screen.getByRole("link", { name: /The Matrix/ })).toHaveAttribute("href", href);
});
it("leaves a request row unlinked when its payload lacks the TMDB id", () => {
  showOnly({
    ...row,
    type: "request.approved",
    reason_flags: { title: "No Id", media_type: "movie" },
  });
  renderAt();
  expect(screen.getByText("No Id")).toBeInTheDocument();
  expect(screen.queryByRole("link", { name: /No Id/ })).not.toBeInTheDocument();
});
it("does not describe failed initial loading as an empty inbox or permit an unbounded read-all", () => {
  state.list.data = undefined;
  state.list.isError = true;
  renderAt();
  expect(screen.queryByText("No notifications yet")).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Mark all read" })).toBeDisabled();
  expect(screen.getByRole("button", { name: "Reload notifications" })).toBeInTheDocument();
});

const prefs = {
  profile_id: "owner",
  enabled: true,
  notify_favorites: true,
  notify_watchlist: false,
  notify_continue_watching: true,
  notify_next_up: true,
};
const reasons = (set: Partial<Record<string, boolean>>) => ({
  favorite: false,
  watchlist: false,
  continue_watching: false,
  next_up: false,
  ...set,
});
function seriesQuery(series: unknown[]) {
  return { data: { pages: [{ series }] }, isLoading: false, isError: false, hasNextPage: false };
}
function seriesEntry(id: string, title: string, entry: Record<string, unknown>) {
  return {
    item: { content_id: id, title, type: "series", status: "matched", genres: [], keywords: [] },
    ...entry,
  };
}
it("offers the Series view only when the server lists notified series", () => {
  renderAt("/notifications?view=series");
  expect(screen.queryByRole("button", { name: "Series" })).not.toBeInTheDocument();
  expect(screen.getByText("Displayed episode")).toBeInTheDocument();
});
it("searches, filters, and sorts the Series view from the URL", async () => {
  state.capability = { series: { available: true } };
  state.series = { active: seriesQuery([]), inactive: seriesQuery([]) };
  renderAt("/notifications?view=series&reason=watchlist&sort=-year");
  expect(state.seriesOptions.at(-1)).toEqual({ q: "", reason: "watchlist", sort: "-year" });
  expect(screen.getByRole("combobox", { name: "Filter by reason" })).toHaveTextContent("Watchlist");
  expect(screen.getByRole("combobox", { name: "Sort" })).toHaveTextContent("Release year");
  expect(screen.getByText("No series match. Try another search or reason.")).toBeInTheDocument();
  fireEvent.change(screen.getByRole("searchbox", { name: "Search series" }), {
    target: { value: " bear " },
  });
  await waitFor(() =>
    expect(state.seriesOptions.at(-1)).toEqual({ q: "bear", reason: "watchlist", sort: "-year" }),
  );
});
it("pages the notifying series before showing the silenced ones", () => {
  state.capability = { series: { available: true } };
  state.prefs = prefs;
  const active = {
    ...seriesQuery([
      seriesEntry("series:bear", "The Bear", {
        reasons: reasons({ favorite: true }),
        active_reasons: reasons({ favorite: true }),
        notifies: true,
      }),
    ]),
    hasNextPage: true,
    fetchNextPage: vi.fn(),
  };
  const inactive = {
    ...seriesQuery([
      seriesEntry("series:slow", "Slow Horses", {
        reasons: reasons({ watchlist: true }),
        active_reasons: reasons({}),
        notifies: false,
      }),
    ]),
    hasNextPage: true,
    fetchNextPage: vi.fn(),
  };
  state.series = { active, inactive };
  renderAt("/notifications?view=series");
  expect(screen.queryByText("Slow Horses")).not.toBeInTheDocument();
  // jsdom has no IntersectionObserver, so the foot offers a button.
  fireEvent.click(screen.getByRole("button", { name: "Load more" }));
  expect(active.fetchNextPage).toHaveBeenCalledOnce();
  expect(inactive.fetchNextPage).not.toHaveBeenCalled();
});
it("keeps loading when a page resolves to no visible series", () => {
  state.capability = { series: { available: true } };
  const active = { ...seriesQuery([]), hasNextPage: true, fetchNextPage: vi.fn() };
  state.series = { active, inactive: seriesQuery([]) };
  renderAt("/notifications?view=series");
  expect(screen.queryByText("No series notify you yet")).not.toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "Load more" }));
  expect(active.fetchNextPage).toHaveBeenCalledOnce();
});
it("mutes a series from its row and lists muted series under Won't notify with Unmute", () => {
  state.capability = { series: { available: true, mute: true } };
  state.prefs = prefs;
  state.series = {
    active: seriesQuery([
      seriesEntry("series:andor", "Andor", {
        reasons: reasons({ favorite: true }),
        active_reasons: reasons({ favorite: true }),
        notifies: true,
        muted: false,
      }),
    ]),
    inactive: seriesQuery([
      seriesEntry("series:wire", "The Wire", {
        reasons: reasons({ favorite: true }),
        active_reasons: reasons({ favorite: true }),
        notifies: false,
        muted: true,
      }),
    ]),
  };
  renderAt("/notifications?view=series");
  fireEvent.click(screen.getByRole("button", { name: "Mute Andor" }));
  expect(state.mute.mutate).toHaveBeenLastCalledWith({
    seriesId: "series:andor",
    title: "Andor",
    muted: true,
  });
  const wire = screen.getByRole("link", { name: /The Wire/ });
  expect(wire).toHaveTextContent("Muted");
  expect(wire).toHaveTextContent("Favorite (off)");
  expect(screen.queryByRole("button", { name: "Mute The Wire" })).not.toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "Unmute The Wire" }));
  expect(state.mute.mutate).toHaveBeenLastCalledWith({
    seriesId: "series:wire",
    title: "The Wire",
    muted: false,
  });
});
it("offers no Mute when the server cannot mute a series", () => {
  state.capability = { series: { available: true } };
  state.series = {
    active: seriesQuery([
      seriesEntry("series:andor", "Andor", {
        reasons: reasons({ favorite: true }),
        active_reasons: reasons({ favorite: true }),
        notifies: true,
      }),
    ]),
    inactive: seriesQuery([]),
  };
  renderAt("/notifications?view=series");
  expect(screen.getByRole("link", { name: /Andor/ })).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: /Mute/ })).not.toBeInTheDocument();
});
