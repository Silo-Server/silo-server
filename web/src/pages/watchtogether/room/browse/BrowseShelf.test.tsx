import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useState } from "react";
import type { RoomPickerResponse } from "@/api/v2/watchTogetherPicker";
import type { RoomMemberStateResponse } from "@/api/v2/watchTogetherMemberState";
import type { EpisodeListItem } from "@/api/types";
import { BrowseShelf, type BrowseSelection } from "./BrowseShelf";
import { CandidateStage, type CandidateChoice } from "./CandidateStage";

const data = vi.hoisted(() => ({
  picker: null as unknown as RoomPickerResponse,
  memberState: null as unknown as RoomMemberStateResponse,
  memberStateCalls: [] as string[][],
  search: [] as { content_id: string; type: string; title: string; year?: number }[],
  home: [] as { content_id: string; type: string; title: string; year?: number }[],
}));
vi.mock("@/lib/watchTogether", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/watchTogether")>()),
  getWatchTogetherRoomPicker: vi.fn(async () => data.picker),
  queryWatchTogetherMemberState: vi.fn(async (_r: string, _t: string, ids: string[]) => {
    data.memberStateCalls.push(ids);
    return data.memberState;
  }),
}));
vi.mock("@/hooks/queries/catalog", () => ({
  createCatalogSearchState: (source: string, patch: object) => ({ source, ...patch }),
  fetchCatalogPage: vi.fn(async (state: { q?: string }) => ({
    items: state.q ? data.search : [],
    total: state.q ? data.search.length : 0,
    has_more: false,
  })),
}));
vi.mock("@/hooks/useDebounce", () => ({ useDebounce: (v: string) => v }));
vi.mock("@/hooks/queries/sections", () => ({
  HOME_SECTION_STALE_TIME: 0,
  HOME_SECTION_GC_TIME: 0,
  useHomeLayout: () => ({
    data: {
      sections: [
        { id: "cw", section_type: "continue_watching", title: "Continue Watching" },
        { id: "trend", section_type: "collection", title: "Trending Series This Week" },
      ],
    },
    isFetching: false,
  }),
  fetchHomeSectionItems: vi.fn(async (id: string) => ({
    section: {
      id,
      section_type: "collection",
      title: "Trending Series This Week",
      items: data.home,
    },
  })),
}));
// Embla needs layout and matchMedia; the carousel has its own tests.
vi.mock("@/components/MediaCarousel", () => ({
  default: ({
    children,
    title,
    headerActions,
  }: {
    children: React.ReactNode;
    title: string;
    headerActions?: React.ReactNode;
  }) => (
    <section>
      <h3>{title}</h3>
      {headerActions}
      <div>{children}</div>
    </section>
  ),
}));
vi.mock("@/hooks/queries/catalogRead", () => ({
  useCatalogItemDetail: (id?: string) => ({
    isLoading: false,
    data: id
      ? {
          content_id: id,
          type: "movie",
          title: "Arrival",
          year: 2016,
          runtime: 116,
          content_rating: "PG-13",
          genres: ["Drama", "Science Fiction"],
          overview: "A linguist is recruited to communicate with alien visitors.",
          rating_imdb: 7.9,
          rating_tmdb: null,
          rating_rt_critic: null,
          poster_url: "",
          poster_thumbhash: "",
          backdrop_url: "",
        }
      : undefined,
  }),
}));
const episodes: EpisodeListItem[] = [1, 2, 3, 4].map(
  (n): EpisodeListItem => ({
    content_id: `sev-s2e${n}`,
    season_number: 2,
    episode_number: n,
    title: `Episode ${n}`,
    overview: "",
    air_date: null,
    runtime: 50,
    still_url: "",
    still_thumbhash: "",
    files: [
      {
        file_id: n,
        resolution: "1080p",
        codec_video: "h264",
        hdr: false,
        audio_channels: 6,
        container: "mkv",
        file_size: 1,
      },
    ],
  }),
);
vi.mock("@/hooks/queries/episodes", () => ({
  useSeasons: () => ({
    data: {
      seasons: [
        {
          content_id: "s2",
          season_number: 2,
          is_specials: false,
          title: "S2",
          overview: "",
          air_date: null,
          episode_count: 4,
          poster_url: "",
          poster_thumbhash: "",
          user_data: { watched_count: 3, unplayed_count: 1, in_progress_count: 1 },
        },
      ],
    },
  }),
  useSeasonEpisodes: (id?: string, season?: number) => ({
    data: id && season === 2 ? { episodes } : { episodes: [] },
    isLoading: false,
  }),
}));

const members = [
  {
    user_id: 1,
    profile_id: "p1",
    display_name: "Nathan",
    is_host: true,
    is_self: true,
    connected: true,
    lobby_ready: false,
  },
  {
    user_id: 2,
    profile_id: "p2",
    display_name: "Maya",
    is_host: false,
    is_self: false,
    connected: true,
    lobby_ready: false,
  },
  {
    user_id: 3,
    profile_id: "p3",
    display_name: "Theo",
    is_host: false,
    is_self: false,
    connected: true,
    lobby_ready: false,
  },
];
const wireMembers = members.map((m) => ({ ...m, user_id: String(m.user_id) }));
function stateFor(id: string, states: Record<number, "unseen" | "in_progress" | "watched">) {
  return {
    content_id: id,
    members: members.map((m) => ({
      user_id: m.user_id,
      profile_id: m.profile_id,
      state: states[m.user_id] ?? "unseen",
      on_watchlist: false,
    })),
  };
}

beforeEach(() => {
  data.memberStateCalls = [];
  data.search = [];
  data.picker = {
    members: wireMembers,
    continue_together: [
      {
        item: {
          content_id: "severance",
          type: "series",
          title: "Severance",
          genres: [],
          keywords: [],
          status: "matched",
        },
        members: [
          {
            user_id: 1,
            profile_id: "p1",
            display_name: "Nathan",
            position_seconds: 600,
            duration_seconds: 3000,
          },
          {
            user_id: 2,
            profile_id: "p2",
            display_name: "Maya",
            position_seconds: 1500,
            duration_seconds: 3000,
          },
        ],
        next_up: {
          content_id: "sev-s2e4",
          season_number: 2,
          episode_number: 4,
          title: "Episode 4",
          member_count: 2,
        },
      },
    ],
    watchlist_union: [
      {
        item: {
          content_id: "arrival",
          type: "movie",
          title: "Arrival",
          year: 2016,
          genres: [],
          keywords: [],
          status: "matched",
        },
        members: [{ user_id: 3, profile_id: "p3", display_name: "Theo" }],
      },
    ],
  };
  data.memberState = {
    members: wireMembers,
    items: [
      stateFor("sev-s2e1", { 1: "watched", 2: "watched", 3: "watched" }),
      stateFor("sev-s2e2", { 1: "watched", 2: "watched", 3: "watched" }),
      stateFor("sev-s2e3", { 1: "watched", 2: "watched", 3: "unseen" }),
      stateFor("sev-s2e4", { 1: "in_progress", 2: "in_progress", 3: "unseen" }),
      stateFor("arrival", { 1: "watched", 2: "in_progress", 3: "unseen" }),
    ],
  };
});
afterEach(cleanup);

const client = () => new QueryClient({ defaultOptions: { queries: { retry: false } } });

function renderShelf(over: Partial<React.ComponentProps<typeof BrowseShelf>> = {}) {
  const onSelect = vi.fn<(s: BrowseSelection) => void>();
  function Shelf() {
    const [open, setOpen] = useState(over.open ?? true);
    return (
      <BrowseShelf
        roomId="room"
        roomToken="proof"
        members={members}
        verb="pick"
        onSelect={onSelect}
        {...over}
        open={open}
        onOpenChange={setOpen}
      />
    );
  }
  render(
    <QueryClientProvider client={client()}>
      <Shelf />
    </QueryClientProvider>,
  );
  return onSelect;
}

function renderCandidate(
  selection: BrowseSelection,
  over: Partial<React.ComponentProps<typeof CandidateStage>> = {},
) {
  const onConfirm = vi.fn<(c: CandidateChoice) => void>();
  const onDismiss = vi.fn();
  render(
    <QueryClientProvider client={client()}>
      <CandidateStage
        selection={selection}
        roomId="room"
        roomToken="proof"
        members={members}
        verb="stage"
        busy={false}
        onConfirm={onConfirm}
        onDismiss={onDismiss}
        {...over}
      />
    </QueryClientProvider>,
  );
  return { onConfirm, onDismiss };
}

describe("BrowseShelf", () => {
  it("leads with the together rows and who-tags, and hands a selection up", async () => {
    const onSelect = renderShelf();
    await screen.findByText("Continue watching together");
    expect(screen.getByText("On your watchlists")).toBeInTheDocument();
    expect(screen.getByText("S2 E4")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Severance" })).toHaveAccessibleDescription(
      "Nathan, Maya",
    );
    fireEvent.click(screen.getByRole("button", { name: "Severance" }));
    expect(onSelect).toHaveBeenCalledWith({
      card: expect.objectContaining({ content_id: "severance", type: "series" }),
      season: 2,
    });
  });

  it("adds the home discovery rows with only movies and series, and filters them by chip", async () => {
    data.home = [
      { content_id: "severance-ep", type: "episode", title: "Hello, Ms. Cobel" },
      { content_id: "the-bear", type: "series", title: "The Bear" },
      { content_id: "dune", type: "movie", title: "Dune", year: 2021 },
    ];
    renderShelf();
    await screen.findByText("Trending Series This Week");
    expect(screen.getByRole("button", { name: "The Bear" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Hello, Ms. Cobel" })).toBeNull();
    fireEvent.click(screen.getByRole("tab", { name: "Series" }));
    expect(screen.getByRole("button", { name: "The Bear" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Dune" })).toBeNull();
    expect(screen.queryByText("Continue watching together")).toBeNull();
    data.home = [];
  });

  it("switches to a wrapping result set while searching and says what to try when empty", async () => {
    data.search = [{ content_id: "dune", type: "movie", title: "Dune", year: 2021 }];
    renderShelf();
    await screen.findByText("On your watchlists");
    fireEvent.change(screen.getByRole("textbox", { name: "Search movies and series" }), {
      target: { value: "dune" },
    });
    await screen.findByText("Search results");
    expect(await screen.findByRole("button", { name: "Dune" })).toBeInTheDocument();
    expect(screen.queryByText("On your watchlists")).toBeNull();
    data.search = [];
    fireEvent.change(screen.getByRole("textbox", { name: "Search movies and series" }), {
      target: { value: "zzz" },
    });
    await screen.findByText(/No matches for “zzz”/);
  });

  it("folds to one line when collapsible and reopens on click", () => {
    renderShelf({ collapsible: true, open: false, collapsedLabel: "Change what's up next" });
    fireEvent.click(screen.getByTestId("browse-shelf-collapsed"));
    expect(screen.getByRole("textbox", { name: "Search movies and series" })).toBeInTheDocument();
  });

  it("preserves search and filters while folding and reopening", () => {
    renderShelf({ collapsible: true });
    fireEvent.change(screen.getByRole("textbox", { name: "Search movies and series" }), {
      target: { value: "arrival" },
    });
    fireEvent.click(screen.getByRole("tab", { name: "Movies" }));
    fireEvent.click(screen.getByRole("button", { name: "Hide browse" }));
    fireEvent.click(screen.getByTestId("browse-shelf-collapsed"));
    expect(screen.getByRole("textbox", { name: "Search movies and series" })).toHaveValue(
      "arrival",
    );
    expect(screen.getByRole("tab", { name: "Movies" })).toHaveAttribute("aria-selected", "true");
  });
});

describe("CandidateStage", () => {
  it("previews a movie with who has seen it and confirms as a movie choice", async () => {
    const { onConfirm, onDismiss } = renderCandidate(
      { card: { content_id: "arrival", type: "movie", title: "Arrival", year: 2016 } },
      { replaces: "Dune (2021)" },
    );
    await screen.findByText("Who's seen it");
    expect(screen.getByText("Candidate")).toBeInTheDocument();
    expect(screen.getByText(/1h 56m/)).toBeInTheDocument();
    await screen.findByText("1 of 3");
    expect(screen.getByText("Replaces Dune (2021)")).toBeInTheDocument();
    expect(data.memberStateCalls).toContainEqual(["arrival"]);
    fireEvent.click(screen.getByRole("button", { name: "Stage it for the room" }));
    expect(onConfirm).toHaveBeenCalledWith(
      expect.objectContaining({ content_id: "arrival", content_type: "movie", subtitle: "2016" }),
    );
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(onDismiss).toHaveBeenCalledTimes(1);
  });

  it("drills into a series, warns about spoilers, and confirms an episode from the panel", async () => {
    const { onConfirm } = renderCandidate(
      { card: { content_id: "severance", type: "series", title: "Severance" }, season: 2 },
      { verb: "suggest" },
    );
    await screen.findByText("Episode 4");
    expect(screen.getByText("You · 3 of 4 watched")).toBeInTheDocument();
    await waitFor(() => expect(data.memberStateCalls.length).toBeGreaterThan(0));
    await screen.findByText(/next up for 2 of you/i);
    const suggestButtons = await screen.findAllByRole("button", { name: "Suggest" });
    fireEvent.click(suggestButtons[3]!);
    await screen.findByText(/Theo hasn't seen E3/);
    fireEvent.click(screen.getByRole("button", { name: "Suggest this" }));
    expect(onConfirm).toHaveBeenCalledWith(
      expect.objectContaining({
        content_id: "sev-s2e4",
        content_type: "episode",
        subtitle: "Severance · S2 E4",
      }),
    );
  });

  it("dismisses on Escape", () => {
    const { onDismiss } = renderCandidate({
      card: { content_id: "arrival", type: "movie", title: "Arrival" },
    });
    fireEvent.keyDown(window, { key: "Escape" });
    expect(onDismiss).toHaveBeenCalledTimes(1);
  });
});
