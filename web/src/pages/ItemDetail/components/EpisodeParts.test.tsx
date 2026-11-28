// @vitest-environment jsdom
import { afterEach, expect, it } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";

import type { EpisodeListItem } from "@/api/types";
import EpisodeParts from "./EpisodeParts";

afterEach(cleanup);

// The first tab can show the series description, which spoiler protection
// leaves visible, while a later episode's description stays hidden until the
// viewer reveals it.
it("keeps a later episode's description hidden when the first tab is visible", () => {
  const episode = {
    content_id: "e15",
    season_number: 12,
    episode_number: 15,
    title: "Part one",
    overview: "",
    spanned_episodes: [
      { content_id: "e16", episode_number: 16, title: "Part two", overview: "Charlie is alive." },
    ],
  } as unknown as EpisodeListItem;
  render(
    <EpisodeParts
      episode={episode}
      firstOverview="A series about two brothers."
      firstHidden={false}
      hideOverviews
    />,
  );
  expect(screen.getByText("A series about two brothers.")).toBeTruthy();

  fireEvent.click(screen.getByRole("tab", { name: /Episode 16/ }));
  expect(screen.queryByText("Charlie is alive.")).toBeNull();
  expect(screen.getByText(/Description hidden until you watch/)).toBeTruthy();
});

it("moves between episode tabs with the arrow keys", () => {
  const episode = {
    content_id: "e4",
    season_number: 1,
    episode_number: 4,
    title: "Aliens of London (1)",
    overview: "Rose is taken home.",
    spanned_episodes: [
      {
        content_id: "e5",
        episode_number: 5,
        title: "World War Three (2)",
        overview: "Time runs out.",
      },
    ],
  } as unknown as EpisodeListItem;
  render(<EpisodeParts episode={episode} hideOverviews={false} />);
  const first = screen.getByRole("tab", { name: /Episode 4/ });
  first.focus();
  fireEvent.keyDown(first, { key: "ArrowRight" });

  const second = screen.getByRole("tab", { name: /Episode 5/ });
  expect(second.getAttribute("aria-selected")).toBe("true");
  expect(document.activeElement).toBe(second);
  expect(screen.getByText("Time runs out.")).toBeTruthy();
  expect(first.getAttribute("tabindex")).toBe("-1");
});
