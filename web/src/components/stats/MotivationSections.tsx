import { useRef, useState, type FormEvent } from "react";

import { ApiClientError } from "@/api/client";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Progress } from "@/components/ui/progress";
import {
  useSaveReadingGoals,
  type ReadingMotivationAchievement,
  type ReadingMotivationChallenge,
  type ReadingMotivationDNA,
  type ReadingMotivationGoals,
  type ReadingMotivationStreak,
} from "@/hooks/queries/readingStats";
import { formatDate } from "@/lib/datetime";
import { formatDuration } from "@/lib/formatDuration";
import { cn } from "@/lib/utils";

// Yearly goal bounds, mirrored client-side from HandlePutGoals so an
// obviously invalid value is caught before round-tripping to the server.
const MIN_YEARLY_GOAL = 1;
const MAX_YEARLY_GOAL = 100000;
const GOAL_RANGE_ERROR = `Enter a number between ${MIN_YEARLY_GOAL} and ${MAX_YEARLY_GOAL}.`;

function EmptyCard({ title, message }: { title: string; message: string }) {
  return (
    <Card className="rounded-2xl border-0 shadow-none">
      <CardHeader>
        <CardTitle className="text-base font-semibold">{title}</CardTitle>
      </CardHeader>
      <CardContent>
        <p className="text-muted-foreground text-sm">{message}</p>
      </CardContent>
    </Card>
  );
}

export interface StreakChallengeSectionProps {
  streak: ReadingMotivationStreak | null | undefined;
  challenge: ReadingMotivationChallenge | null | undefined;
}

export function StreakChallengeSection({ streak, challenge }: StreakChallengeSectionProps) {
  if (!streak && !challenge) {
    return (
      <EmptyCard
        title="Streaks & challenge"
        message="No streak data yet — start reading to build one up."
      />
    );
  }

  return (
    <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
      <Card className="rounded-2xl border-0 shadow-none">
        <CardHeader>
          <CardTitle className="text-base font-semibold">Reading streak</CardTitle>
        </CardHeader>
        <CardContent>
          {streak ? (
            <div className="space-y-1">
              <div className="text-3xl font-semibold tracking-tight">
                {streak.current_days} {streak.current_days === 1 ? "day" : "days"}
              </div>
              <p className="text-muted-foreground text-sm">
                Longest streak: {streak.longest_days} {streak.longest_days === 1 ? "day" : "days"}
              </p>
              <p className="text-muted-foreground text-xs">
                {streak.today_qualified
                  ? "Today counts toward your streak."
                  : `${formatDuration(streak.today_seconds)} read today.`}
              </p>
            </div>
          ) : (
            <p className="text-muted-foreground text-sm">No streak data yet.</p>
          )}
        </CardContent>
      </Card>

      <Card className="rounded-2xl border-0 shadow-none">
        <CardHeader>
          <CardTitle className="text-base font-semibold">Monthly challenge</CardTitle>
        </CardHeader>
        <CardContent>
          {challenge ? (
            <div className="space-y-2">
              <p className="text-muted-foreground text-sm">
                {formatDuration(challenge.month_seconds)} of{" "}
                {formatDuration(challenge.target_seconds)}
              </p>
              <Progress value={challenge.percent} />
              <p className="text-muted-foreground text-xs">{challenge.percent}% complete</p>
            </div>
          ) : (
            <p className="text-muted-foreground text-sm">No challenge data yet.</p>
          )}
        </CardContent>
      </Card>
    </div>
  );
}

export interface GoalsSectionProps {
  goals: ReadingMotivationGoals | null | undefined;
}

function goalDisplayValue(value: number | null): string {
  return value == null ? "" : String(value);
}

/** Parses a goal input's raw text: "" -> cleared (null), else a validated integer. */
function parseGoalInput(raw: string): number | null | "invalid" {
  const trimmed = raw.trim();
  if (trimmed === "") return null;
  const parsed = Number(trimmed);
  if (!Number.isInteger(parsed) || parsed < MIN_YEARLY_GOAL || parsed > MAX_YEARLY_GOAL) {
    return "invalid";
  }
  return parsed;
}

export function GoalsSection({ goals }: GoalsSectionProps) {
  if (!goals) {
    return (
      <EmptyCard
        title="Reading goals"
        message="No goals data yet — set a books or hours target below once it loads."
      />
    );
  }

  // Remounted (via key, below) whenever the server's saved values change, so
  // each instance's local input/error state simply starts fresh from props
  // instead of needing an effect to re-sync it.
  return (
    <GoalsForm
      key={`${goals.books_per_year ?? "none"}-${goals.hours_per_year ?? "none"}`}
      goals={goals}
    />
  );
}

function GoalsForm({ goals }: { goals: ReadingMotivationGoals }) {
  const savedBooks = goals.books_per_year;
  const savedHours = goals.hours_per_year;

  const [booksInput, setBooksInput] = useState(() => goalDisplayValue(savedBooks));
  const [hoursInput, setHoursInput] = useState(() => goalDisplayValue(savedHours));
  const [booksError, setBooksError] = useState<string | null>(null);
  const [hoursError, setHoursError] = useState<string | null>(null);
  // Surfaces a failed save (network/server error), distinct from the
  // client-side range validation above. Cleared on the next successful save.
  const [saveError, setSaveError] = useState<string | null>(null);

  const { mutateAsync: saveGoals } = useSaveReadingGoals();

  // Tracks the last value actually *persisted*, so a blur that didn't change
  // anything skips the PUT, and so a PUT for one field can still send the
  // other field's current value. Only advanced after a successful save —
  // if the PUT fails, the ref stays put so the next blur (even with the same
  // unsaved value) retries instead of silently no-opping forever.
  const savedRef = useRef({ books: savedBooks, hours: savedHours });

  function saveFailureMessage(err: unknown): string {
    return err instanceof ApiClientError ? err.message : "Failed to save reading goals.";
  }

  async function commitBooks() {
    const parsed = parseGoalInput(booksInput);
    if (parsed === "invalid") {
      setBooksError(GOAL_RANGE_ERROR);
      return;
    }
    setBooksError(null);
    if (parsed === savedRef.current.books) return;
    try {
      await saveGoals({ books_per_year: parsed, hours_per_year: savedRef.current.hours });
      savedRef.current.books = parsed;
      setSaveError(null);
    } catch (err) {
      setSaveError(saveFailureMessage(err));
    }
  }

  async function commitHours() {
    const parsed = parseGoalInput(hoursInput);
    if (parsed === "invalid") {
      setHoursError(GOAL_RANGE_ERROR);
      return;
    }
    setHoursError(null);
    if (parsed === savedRef.current.hours) return;
    try {
      await saveGoals({ books_per_year: savedRef.current.books, hours_per_year: parsed });
      savedRef.current.hours = parsed;
      setSaveError(null);
    } catch (err) {
      setSaveError(saveFailureMessage(err));
    }
  }

  function preventSubmit(e: FormEvent) {
    e.preventDefault();
  }

  return (
    <Card className="rounded-2xl border-0 shadow-none">
      <CardHeader>
        <CardTitle className="text-base font-semibold">Reading goals</CardTitle>
      </CardHeader>
      <CardContent>
        <form className="grid grid-cols-1 gap-4 sm:grid-cols-2" onSubmit={preventSubmit}>
          <div className="space-y-1.5">
            <label htmlFor="goal-books-per-year" className="text-sm font-medium">
              Books per year
            </label>
            <Input
              id="goal-books-per-year"
              inputMode="numeric"
              value={booksInput}
              aria-invalid={booksError != null}
              onChange={(e) => setBooksInput(e.target.value)}
              onBlur={() => void commitBooks()}
            />
            {booksError ? <p className="text-destructive text-xs">{booksError}</p> : null}
            <p className="text-muted-foreground text-xs">
              {goals.books_finished_ytd} finished this year · on track for{" "}
              {goals.books_on_track_for}
            </p>
          </div>

          <div className="space-y-1.5">
            <label htmlFor="goal-hours-per-year" className="text-sm font-medium">
              Hours per year
            </label>
            <Input
              id="goal-hours-per-year"
              inputMode="numeric"
              value={hoursInput}
              aria-invalid={hoursError != null}
              onChange={(e) => setHoursInput(e.target.value)}
              onBlur={() => void commitHours()}
            />
            {hoursError ? <p className="text-destructive text-xs">{hoursError}</p> : null}
            <p className="text-muted-foreground text-xs">
              {Math.round(goals.hours_ytd)}h this year · on track for {goals.hours_on_track_for}h
            </p>
          </div>
        </form>
        {saveError ? <p className="text-destructive mt-3 text-xs">{saveError}</p> : null}
      </CardContent>
    </Card>
  );
}

export interface AchievementsSectionProps {
  achievements: ReadingMotivationAchievement[] | null | undefined;
}

function groupByCategory(
  achievements: ReadingMotivationAchievement[],
): [string, ReadingMotivationAchievement[]][] {
  const order: string[] = [];
  const groups = new Map<string, ReadingMotivationAchievement[]>();
  for (const achievement of achievements) {
    let bucket = groups.get(achievement.category);
    if (!bucket) {
      bucket = [];
      groups.set(achievement.category, bucket);
      order.push(achievement.category);
    }
    bucket.push(achievement);
  }
  return order.map((category) => [category, groups.get(category)!]);
}

export function AchievementsSection({ achievements }: AchievementsSectionProps) {
  if (!achievements || achievements.length === 0) {
    return (
      <EmptyCard
        title="Achievements"
        message="No achievements yet — badges unlock automatically as you read."
      />
    );
  }

  return (
    <Card className="rounded-2xl border-0 shadow-none">
      <CardHeader>
        <CardTitle className="text-base font-semibold">Achievements</CardTitle>
      </CardHeader>
      <CardContent className="space-y-6">
        {groupByCategory(achievements).map(([category, items]) => (
          <div key={category}>
            <h3 className="text-muted-foreground mb-2 text-sm font-semibold capitalize">
              {category}
            </h3>
            <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 md:grid-cols-4">
              {items.map((achievement) => {
                const locked = !achievement.achieved_at;
                return (
                  <div
                    key={achievement.id}
                    data-locked={locked ? "true" : undefined}
                    aria-disabled={locked}
                    className={cn(
                      "rounded-lg border p-3 text-sm",
                      locked ? "border-border/60 opacity-50" : "border-primary/40",
                    )}
                  >
                    <div className="font-medium">{achievement.name}</div>
                    <div className="text-muted-foreground text-xs">{achievement.description}</div>
                    {achievement.achieved_at ? (
                      <div className="text-muted-foreground mt-1 text-[11px] tabular-nums">
                        {formatDate(achievement.achieved_at, "medium")}
                      </div>
                    ) : null}
                  </div>
                );
              })}
            </div>
          </div>
        ))}
      </CardContent>
    </Card>
  );
}

export interface ReadingDnaSectionProps {
  dna: ReadingMotivationDNA | null | undefined;
}

const HOUR_BUCKET_ORDER = ["morning", "afternoon", "evening", "night"];

export function ReadingDnaSection({ dna }: ReadingDnaSectionProps) {
  if (!dna || (dna.genres.length === 0 && dna.authors.length === 0)) {
    return (
      <EmptyCard
        title="Reading DNA"
        message="No reading DNA yet — genre and author breakdowns appear once you've read a bit."
      />
    );
  }

  const maxGenreSeconds = dna.genres.reduce((max, g) => Math.max(max, g.seconds), 0) || 1;
  const bucketEntries = HOUR_BUCKET_ORDER.filter((bucket) => bucket in dna.hours_by_bucket).map(
    (bucket) => [bucket, dna.hours_by_bucket[bucket]!] as const,
  );

  return (
    <Card className="rounded-2xl border-0 shadow-none">
      <CardHeader>
        <CardTitle className="text-base font-semibold">Reading DNA</CardTitle>
      </CardHeader>
      <CardContent className="space-y-6">
        {dna.genres.length > 0 ? (
          <div>
            <h3 className="text-muted-foreground mb-2 text-sm font-semibold">Top genres</h3>
            <ul className="space-y-2">
              {dna.genres.map((genre) => (
                <li key={genre.name} className="space-y-1">
                  <div className="flex items-center justify-between text-sm">
                    <span className="font-medium">{genre.name}</span>
                    <span className="text-muted-foreground tabular-nums">
                      {formatDuration(genre.seconds)}
                    </span>
                  </div>
                  <div className="bg-muted h-1.5 overflow-hidden rounded-sm">
                    <div
                      className="bg-primary h-full"
                      style={{ width: `${Math.round((genre.seconds / maxGenreSeconds) * 100)}%` }}
                    />
                  </div>
                </li>
              ))}
            </ul>
          </div>
        ) : null}

        {dna.authors.length > 0 ? (
          <div>
            <h3 className="text-muted-foreground mb-2 text-sm font-semibold">Top authors</h3>
            <ul className="divide-border/60 divide-y text-sm">
              {dna.authors.map((author) => (
                <li key={author.name} className="flex items-center justify-between py-1.5">
                  <span>{author.name}</span>
                  <span className="text-muted-foreground tabular-nums">
                    {formatDuration(author.seconds)}
                  </span>
                </li>
              ))}
            </ul>
          </div>
        ) : null}

        <div className="grid grid-cols-2 gap-4 sm:grid-cols-4">
          <div>
            <div className="text-muted-foreground text-xs">Diversity</div>
            <div className="text-xl font-semibold tabular-nums">{dna.diversity_score}</div>
          </div>
          {bucketEntries.map(([bucket, hours]) => (
            <div key={bucket}>
              <div className="text-muted-foreground text-xs capitalize">{bucket}</div>
              <div className="text-xl font-semibold tabular-nums">{hours}h</div>
            </div>
          ))}
        </div>

        <p className="text-muted-foreground text-sm">
          On pace for {Math.round(dna.projected_year_hours)}h this year.
        </p>
      </CardContent>
    </Card>
  );
}
