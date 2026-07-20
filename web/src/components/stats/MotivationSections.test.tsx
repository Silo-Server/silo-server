import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderToStaticMarkup } from "react-dom/server";
import { beforeEach, describe, expect, it, vi } from "vitest";

const mockPutReadingGoals = vi.fn();

vi.mock("@/hooks/queries/readingStats", () => ({
  putReadingGoals: (...args: unknown[]) => mockPutReadingGoals(...args),
}));

import {
  AchievementsSection,
  GoalsSection,
  ReadingDnaSection,
  StreakChallengeSection,
} from "./MotivationSections";
import type {
  ReadingMotivationAchievement,
  ReadingMotivationChallenge,
  ReadingMotivationDNA,
  ReadingMotivationGoals,
  ReadingMotivationStreak,
} from "@/hooks/queries/readingStats";

const streakFixture: ReadingMotivationStreak = {
  current_days: 5,
  longest_days: 12,
  today_seconds: 900,
  today_qualified: true,
};

const challengeFixture: ReadingMotivationChallenge = {
  target_seconds: 10000,
  month_seconds: 4000,
  percent: 40,
};

const goalsFixture: ReadingMotivationGoals = {
  books_per_year: 24,
  hours_per_year: 100,
  books_finished_ytd: 6,
  hours_ytd: 42.5,
  books_on_track_for: 12,
  hours_on_track_for: 85,
};

function achievement(
  overrides: Partial<ReadingMotivationAchievement> & Pick<ReadingMotivationAchievement, "id">,
): ReadingMotivationAchievement {
  return {
    category: "time",
    name: overrides.id,
    description: `Description for ${overrides.id}`,
    achieved_at: null,
    ...overrides,
  };
}

const eighteenAchievements: ReadingMotivationAchievement[] = [
  achievement({ id: "first-hour", category: "time", achieved_at: "2026-01-05T00:00:00Z" }),
  achievement({ id: "ten-hours", category: "time" }),
  achievement({ id: "fifty-hours", category: "time" }),
  achievement({ id: "hundred-hours", category: "time" }),
  achievement({ id: "marathon-session", category: "time" }),
  achievement({ id: "streak-3", category: "streak", achieved_at: "2026-02-01T00:00:00Z" }),
  achievement({ id: "streak-7", category: "streak" }),
  achievement({ id: "streak-30", category: "streak" }),
  achievement({ id: "streak-100", category: "streak" }),
  achievement({ id: "first-book", category: "books", achieved_at: "2026-03-01T00:00:00Z" }),
  achievement({ id: "ten-books", category: "books" }),
  achievement({ id: "fifty-books", category: "books" }),
  achievement({ id: "night-owl", category: "habits" }),
  achievement({ id: "early-bird", category: "habits" }),
  achievement({ id: "weekender", category: "habits" }),
  achievement({ id: "genre-hopper", category: "exploration" }),
  achievement({ id: "deep-diver", category: "exploration" }),
  achievement({ id: "finisher", category: "exploration" }),
];

const dnaFixture: ReadingMotivationDNA = {
  genres: [
    { name: "Sci-Fi", seconds: 7200 },
    { name: "Fantasy", seconds: 3600 },
  ],
  authors: [
    { name: "Andy Weir", seconds: 5400 },
    { name: "N.K. Jemisin", seconds: 1800 },
  ],
  diversity_score: 62,
  avg_session_seconds: 1500,
  hours_by_bucket: { morning: 2, afternoon: 1, evening: 4, night: 0 },
  projected_year_hours: 87.3,
};

describe("StreakChallengeSection", () => {
  it("shows current/longest streak and the challenge percent bar width", () => {
    render(<StreakChallengeSection streak={streakFixture} challenge={challengeFixture} />);
    expect(screen.getByText("5 days")).toBeInTheDocument();
    expect(screen.getByText(/Longest streak: 12 days/)).toBeInTheDocument();

    const bar = screen.getByRole("progressbar");
    expect(bar).toHaveAttribute("aria-valuenow", "40");
  });

  it("renders an empty state when both streak and challenge are missing", () => {
    render(<StreakChallengeSection streak={null} challenge={undefined} />);
    expect(screen.getByText(/No streak data yet/)).toBeInTheDocument();
  });
});

describe("GoalsSection", () => {
  beforeEach(() => {
    mockPutReadingGoals.mockReset();
    mockPutReadingGoals.mockResolvedValue(undefined);
  });

  it("prefills inputs from the goals payload", () => {
    render(<GoalsSection goals={goalsFixture} />);
    expect(screen.getByLabelText(/Books per year/)).toHaveValue("24");
    expect(screen.getByLabelText(/Hours per year/)).toHaveValue("100");
  });

  it("calls putReadingGoals on blur with a changed value, sending both current fields", async () => {
    const user = userEvent.setup();
    render(<GoalsSection goals={goalsFixture} />);

    const booksInput = screen.getByLabelText(/Books per year/);
    await user.clear(booksInput);
    await user.type(booksInput, "30");
    await user.tab();

    expect(mockPutReadingGoals).toHaveBeenCalledWith({
      books_per_year: 30,
      hours_per_year: 100,
    });
  });

  it("does not call putReadingGoals on blur when the value is unchanged", async () => {
    const user = userEvent.setup();
    render(<GoalsSection goals={goalsFixture} />);

    const booksInput = screen.getByLabelText(/Books per year/);
    await user.click(booksInput);
    await user.tab();

    expect(mockPutReadingGoals).not.toHaveBeenCalled();
  });

  it("shows an inline error and does not call putReadingGoals for an invalid (0) value", async () => {
    const user = userEvent.setup();
    render(<GoalsSection goals={goalsFixture} />);

    const booksInput = screen.getByLabelText(/Books per year/);
    await user.clear(booksInput);
    await user.type(booksInput, "0");
    await user.tab();

    expect(await screen.findByText(/between 1 and 100000/)).toBeInTheDocument();
    expect(mockPutReadingGoals).not.toHaveBeenCalled();
  });

  it("shows an inline error and does not call putReadingGoals for a negative value", async () => {
    const user = userEvent.setup();
    render(<GoalsSection goals={goalsFixture} />);

    const hoursInput = screen.getByLabelText(/Hours per year/);
    await user.clear(hoursInput);
    await user.type(hoursInput, "-5");
    await user.tab();

    expect(await screen.findByText(/between 1 and 100000/)).toBeInTheDocument();
    expect(mockPutReadingGoals).not.toHaveBeenCalled();
  });

  it("renders an empty state when goals is null", () => {
    render(<GoalsSection goals={null} />);
    expect(screen.getByText(/No goals data yet/)).toBeInTheDocument();
  });
});

describe("AchievementsSection", () => {
  it("renders all 18 achievements grouped by category", () => {
    const markup = renderToStaticMarkup(
      <AchievementsSection achievements={eighteenAchievements} />,
    );
    for (const a of eighteenAchievements) {
      expect(markup).toContain(a.description);
    }
  });

  it("dims locked achievements with data-locked and aria-disabled", () => {
    render(<AchievementsSection achievements={eighteenAchievements} />);
    const locked = document.querySelector('[data-locked="true"]');
    expect(locked).not.toBeNull();
    expect(locked).toHaveAttribute("aria-disabled", "true");
  });

  it("shows the achieved date for achieved badges", () => {
    render(<AchievementsSection achievements={eighteenAchievements} />);
    // first-hour achieved_at is 2026-01-05T00:00:00Z
    expect(screen.getByText("Jan 5, 2026")).toBeInTheDocument();
  });

  it("renders an empty state when achievements is null", () => {
    render(<AchievementsSection achievements={null} />);
    expect(screen.getByText(/No achievements yet/)).toBeInTheDocument();
  });

  it("renders an empty state when achievements is an empty array", () => {
    render(<AchievementsSection achievements={[]} />);
    expect(screen.getByText(/No achievements yet/)).toBeInTheDocument();
  });
});

describe("ReadingDnaSection", () => {
  it("renders top genre bars, authors, diversity score, buckets, and projection", () => {
    render(<ReadingDnaSection dna={dnaFixture} />);
    expect(screen.getByText("Sci-Fi")).toBeInTheDocument();
    expect(screen.getByText("Andy Weir")).toBeInTheDocument();
    expect(screen.getByText("62")).toBeInTheDocument();
    expect(screen.getByText("evening")).toBeInTheDocument();
    expect(screen.getByText(/On pace for 87h this year/)).toBeInTheDocument();
  });

  it("renders an empty state when dna is null", () => {
    render(<ReadingDnaSection dna={null} />);
    expect(screen.getByText(/No reading DNA yet/)).toBeInTheDocument();
  });

  it("renders an empty state when both genres and authors are empty", () => {
    render(<ReadingDnaSection dna={{ ...dnaFixture, genres: [], authors: [] }} />);
    expect(screen.getByText(/No reading DNA yet/)).toBeInTheDocument();
  });
});
