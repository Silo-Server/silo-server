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
