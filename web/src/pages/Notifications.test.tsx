import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
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
  muteMany: { mutate: vi.fn(), isPending: false },
  markMany: { mutate: vi.fn() },
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
  useMarkNotificationsRead: () => state.markMany,
  useNotificationPreferences: () => ({ data: state.prefs, isLoading: false }),
  useUpdateNotificationPreferences: () => ({ mutate: vi.fn() }),
  useSetNotificationSeriesMuted: () => state.mute,
  useSetManyNotificationSeriesMuted: () => state.muteMany,
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
  state.muteMany.mutate.mockReset();
  state.markMany.mutate.mockReset();
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
  notify_next_up: true,
};
const reasons = (set: Partial<Record<string, boolean>>) => ({ next_up: false, ...set });
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
  state.capability = { series: { available: true, mute: true, bulk_mute: true } };
  state.series = { all: seriesQuery([]), muted: seriesQuery([]) };
  renderAt("/notifications?view=series&filter=muted&sort=-year");
  expect(state.seriesOptions.at(-1)).toEqual({ q: "", sort: "-year" });
  expect(screen.getByRole("button", { name: "Muted" })).toHaveAttribute("aria-pressed", "true");
  expect(screen.getByRole("combobox", { name: "Sort" })).toHaveTextContent("Release year");
  expect(screen.getByText("No series match. Try another search or filter.")).toBeInTheDocument();
  fireEvent.change(screen.getByRole("searchbox", { name: "Search series" }), {
    target: { value: " bear " },
  });
  await waitFor(() => expect(state.seriesOptions.at(-1)).toEqual({ q: "bear", sort: "-year" }));
});
it("lists only the muted series under the Muted tab", () => {
  state.capability = { series: { available: true, mute: true, bulk_mute: true } };
  const muted = seriesQuery([
    seriesEntry("series:wire", "The Wire", {
      reasons: reasons({ next_up: true }),
      active_reasons: reasons({ next_up: true }),
      notifies: false,
      muted: true,
    }),
  ]);
  state.series = { all: seriesQuery([]), muted };
  renderAt("/notifications?view=series&filter=muted");
  expect(state.seriesOptions.at(-1)).toEqual({ q: "", sort: "title" });
  const wire = screen.getByRole("link", { name: /The Wire/ });
  expect(wire.closest("li")).toHaveTextContent("Muted");
  expect(
    screen.getByRole("switch", { name: "Notify me about new episodes of The Wire" }),
  ).not.toBeChecked();
});
it("keeps loading when a page resolves to no visible series", () => {
  state.capability = { series: { available: true } };
  const all = { ...seriesQuery([]), hasNextPage: true, fetchNextPage: vi.fn() };
  state.series = { all };
  renderAt("/notifications?view=series");
  expect(screen.queryByText("No series notify you yet")).not.toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "Load more" }));
  expect(all.fetchNextPage).toHaveBeenCalledOnce();
});
it("mutes and unmutes a series from its row switch", () => {
  state.capability = { series: { available: true, mute: true } };
  state.prefs = prefs;
  state.series = {
    all: seriesQuery([
      seriesEntry("series:andor", "Andor", {
        reasons: reasons({ next_up: true }),
        active_reasons: reasons({ next_up: true }),
        notifies: true,
        muted: false,
      }),
      seriesEntry("series:wire", "The Wire", {
        reasons: reasons({ next_up: true }),
        active_reasons: reasons({ next_up: true }),
        notifies: false,
        muted: true,
      }),
      seriesEntry("series:bear", "The Bear", {
        reasons: reasons({ next_up: true }),
        active_reasons: reasons({ next_up: true }),
        notifies: false,
        muted: false,
      }),
    ]),
  };
  renderAt("/notifications?view=series");
  // Next Up is on but an episode is waiting: it notifies once caught up.
  expect(screen.getByRole("link", { name: /The Bear/ }).closest("li")).toHaveTextContent(
    "After you catch up",
  );
  fireEvent.click(screen.getByRole("switch", { name: "Notify me about new episodes of Andor" }));
  expect(state.mute.mutate).toHaveBeenLastCalledWith({
    seriesId: "series:andor",
    title: "Andor",
    muted: true,
  });
  expect(screen.getByRole("link", { name: /The Wire/ }).closest("li")).toHaveTextContent("Muted");
  fireEvent.click(screen.getByRole("switch", { name: "Notify me about new episodes of The Wire" }));
  expect(state.mute.mutate).toHaveBeenLastCalledWith({
    seriesId: "series:wire",
    title: "The Wire",
    muted: false,
  });
});
it("offers no switch or selection when the server cannot mute a series", () => {
  state.capability = { series: { available: true } };
  state.series = {
    all: seriesQuery([
      seriesEntry("series:andor", "Andor", {
        reasons: reasons({ next_up: true }),
        active_reasons: reasons({ next_up: true }),
        notifies: true,
      }),
    ]),
  };
  renderAt("/notifications?view=series");
  expect(screen.getByRole("link", { name: /Andor/ })).toBeInTheDocument();
  expect(screen.queryByRole("switch")).not.toBeInTheDocument();
  expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();
});
it("mutes the selected series together", () => {
  state.capability = { series: { available: true, mute: true, bulk_mute: true } };
  const entry = (id: string, title: string) =>
    seriesEntry(id, title, {
      reasons: reasons({ next_up: true }),
      active_reasons: reasons({ next_up: true }),
      notifies: true,
      muted: false,
    });
  state.series = {
    all: seriesQuery([entry("series:andor", "Andor"), entry("series:bear", "The Bear")]),
  };
  renderAt("/notifications?view=series");
  fireEvent.click(screen.getByRole("checkbox", { name: "Select Andor" }));
  fireEvent.click(screen.getByRole("checkbox", { name: "Select The Bear" }));
  const bar = screen.getByRole("toolbar", { name: "Selected series" });
  expect(bar).toHaveTextContent("2 selected");
  fireEvent.click(within(bar).getByRole("button", { name: "Mute" }));
  expect(state.muteMany.mutate).toHaveBeenCalledWith(
    { seriesIds: ["series:andor", "series:bear"], muted: true },
    expect.anything(),
  );
});

it("offers no Muted tab and no selection while the previous filter's rows show", () => {
  state.capability = { series: { available: true, mute: true } };
  state.series = { all: seriesQuery([]) };
  renderAt("/notifications?view=series&filter=muted");
  expect(screen.queryByRole("button", { name: "Muted" })).not.toBeInTheDocument();
  expect(state.seriesOptions.at(-1)).toEqual({ q: "", sort: "title" });
  cleanup();

  state.capability = { series: { available: true, mute: true, bulk_mute: true } };
  state.series = {
    all: {
      ...seriesQuery([
        seriesEntry("series:andor", "Andor", {
          reasons: reasons({ next_up: true }),
          active_reasons: reasons({ next_up: true }),
          notifies: true,
          muted: false,
        }),
      ]),
      isPlaceholderData: true,
    },
  };
  renderAt("/notifications?view=series");
  expect(screen.getByRole("checkbox", { name: "Select Andor" })).toBeDisabled();
  expect(screen.getByRole("checkbox", { name: "Select all shown series" })).toBeDisabled();
});

/** An episode notification of a series, unread unless read_at is given. */
function episode(id: string, series: string, episodeNumber: number, extra = {}) {
  return {
    ...row,
    id,
    series_id: series,
    series_title: series === "series:abbott" ? "Abbott Elementary" : "The Bear",
    season_number: 5,
    episode_number: episodeNumber,
    episode_title: `Episode ${episodeNumber}`,
    created_at: new Date().toISOString(),
    ...extra,
  };
}
it("groups a series' new episodes into one row that opens to list them", () => {
  state.list.data = {
    pages: [
      {
        notifications: [
          episode("e4", "series:abbott", 4),
          episode("b1", "series:bear", 1),
          episode("e3", "series:abbott", 3),
          episode("e2", "series:abbott", 2, { read_at: "2026-01-01T00:00:00Z" }),
        ],
        read_cutoff: "c",
      },
    ],
  };
  renderAt();
  const group = screen.getByRole("button", { name: /Abbott Elementary/ });
  expect(group).toHaveTextContent("2 new");
  expect(group).toHaveTextContent("S5E2 – S5E4 · latest “Episode 4”");
  expect(screen.queryByText("Episode 3")).not.toBeInTheDocument();
  fireEvent.click(group);
  expect(group).toHaveAttribute("aria-expanded", "true");
  expect(screen.getByRole("link", { name: /Episode 3/ })).toBeInTheDocument();
});
it("marks the selected rows read and mutes their series together", () => {
  state.capability = { series: { available: true, mute: true, bulk_mute: true } };
  state.list.data = {
    pages: [
      {
        notifications: [
          episode("e4", "series:abbott", 4),
          episode("e3", "series:abbott", 3, { read_at: "2026-01-01T00:00:00Z" }),
          episode("b1", "series:bear", 1),
          { ...row, id: "req", type: "request.approved", reason_flags: { title: "Heat" } },
        ],
        read_cutoff: "c",
      },
    ],
  };
  renderAt();
  fireEvent.click(screen.getByRole("button", { name: "Select" }));
  fireEvent.click(screen.getByRole("checkbox", { name: "Abbott Elementary" }));
  fireEvent.click(screen.getByRole("checkbox", { name: "The Bear" }));
  fireEvent.click(screen.getByRole("checkbox", { name: "Heat" }));
  const bar = screen.getByRole("toolbar", { name: "Selected notifications" });
  expect(bar).toHaveTextContent("3 selected");
  expect(bar).toHaveTextContent("4 notifications");
  fireEvent.click(within(bar).getByRole("button", { name: "Mute series" }));
  expect(state.muteMany.mutate).toHaveBeenCalledWith(
    { seriesIds: ["series:abbott", "series:bear"], muted: true },
    expect.anything(),
  );
  fireEvent.click(within(bar).getByRole("button", { name: "Mark read" }));
  expect(state.markMany.mutate).toHaveBeenCalledWith(["e4", "b1", "req"]);
});
