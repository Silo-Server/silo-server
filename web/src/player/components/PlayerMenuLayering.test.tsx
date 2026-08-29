// @vitest-environment jsdom

import { render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import userEvent from "@testing-library/user-event";
import type { ReactElement } from "react";
import { describe, expect, it } from "vitest";
import { AudioTrackMenu } from "./AudioTrackMenu";
import { ChaptersMenu } from "./ChaptersMenu";
import { SubtitleMenu } from "./SubtitleMenu";

async function expectMenuAboveTimeline(triggerName: string, menu: ReactElement) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(<QueryClientProvider client={queryClient}>{menu}</QueryClientProvider>);
  await userEvent.click(screen.getByRole("button", { name: triggerName }));
  expect(screen.getByRole("menu")).toHaveClass("z-30");
}

describe("player menu layering", () => {
  it("keeps the audio menu above timeline overlays", async () => {
    await expectMenuAboveTimeline(
      "Audio tracks",
      <AudioTrackMenu tracks={[{}, {}]} activeIndex={0} currentPosition={0} onSelect={() => {}} />,
    );
  });

  it("keeps the chapters menu above timeline overlays", async () => {
    await expectMenuAboveTimeline(
      "Chapters",
      <ChaptersMenu
        chapters={[
          { index: 0, title: "Chapter 1", start_seconds: 0, end_seconds: 60, source: "test" },
        ]}
        currentTime={0}
        onSeek={() => {}}
      />,
    );
  });

  it("keeps the subtitle menu above timeline overlays", async () => {
    await expectMenuAboveTimeline(
      "Disable captions",
      <SubtitleMenu
        tracks={[{ index: 0, language: "eng", label: "English", url: "/subtitle.vtt" }]}
        activeIndex={0}
        delayMs={0}
        onSelect={() => {}}
        onDelayChange={() => {}}
      />,
    );
  });
});
