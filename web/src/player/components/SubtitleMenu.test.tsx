// @vitest-environment jsdom

import { act, fireEvent, render, screen } from "@testing-library/react";
import { createElement } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { PlayerSubtitleInfo } from "../types";
import { PROVISIONAL_INVENTORY_TIMEOUT_MS } from "../utils/inventoryProvenance";
import { SubtitleMenu } from "./SubtitleMenu";

// The appearance panel reads settings through react-query; the menu's own
// rendering does not need a provider.
vi.mock("./SubtitleAppearancePanel", () => ({
  SubtitleAppearancePanel: () => null,
}));

function subtitleTrack(overrides: Partial<PlayerSubtitleInfo> = {}): PlayerSubtitleInfo {
  return {
    index: 0,
    language: "en",
    codec: "pgs",
    label: "English",
    source: "embedded",
    url: "/stream/session-1/subtitles/0.sup",
    ...overrides,
  };
}

function renderMenu(
  tracks: PlayerSubtitleInfo[],
  onSelect = vi.fn(),
  activeIndex: number | null = null,
) {
  const view = render(
    createElement(SubtitleMenu, {
      tracks,
      activeIndex,
      onSelect,
      delayMs: 0,
      onDelayChange: () => {},
    }),
  );
  fireEvent.click(screen.getByRole("button", { name: /(Enable|Disable) captions/ }));
  return { view, onSelect };
}

describe("SubtitleMenu", () => {
  it("collapses identical probed streams to one row and keeps the first ordinal", () => {
    const { onSelect } = renderMenu([
      subtitleTrack({ index: 13 }),
      subtitleTrack({ index: 14 }),
      subtitleTrack({ index: 23 }),
    ]);

    const rows = screen.getAllByRole("menuitem");
    // Off + one deduped track + Appearance.
    expect(rows).toHaveLength(3);
    const trackRow = screen.getByRole("menuitem", { name: /English/ });
    expect(trackRow).toBeTruthy();

    fireEvent.click(trackRow);
    expect(onSelect).toHaveBeenCalledWith(13);
  });

  it("keeps tracks that differ in forced or hearing-impaired flags", () => {
    renderMenu([
      subtitleTrack({ index: 0 }),
      subtitleTrack({ index: 1, forced: true }),
      subtitleTrack({ index: 2, hearing_impaired: true }),
    ]);

    // Off + three distinct tracks + Appearance.
    expect(screen.getAllByRole("menuitem")).toHaveLength(5);
  });

  it("keeps identically labelled tracks with distinct track ids and selects the second", () => {
    const { onSelect } = renderMenu([
      subtitleTrack({ index: 13, track_id: "file:7:subtitle:13" }),
      subtitleTrack({ index: 14, track_id: "file:7:subtitle:14" }),
    ]);

    // Off + two distinct tracks + Appearance.
    expect(screen.getAllByRole("menuitem")).toHaveLength(4);
    const trackRows = screen.getAllByRole("menuitem", { name: /English/ });
    expect(trackRows).toHaveLength(2);

    fireEvent.click(trackRows[1] as HTMLElement);
    expect(onSelect).toHaveBeenCalledWith(14);
  });

  it("marks the second identically labelled track active when the server selected it", () => {
    renderMenu(
      [
        subtitleTrack({ index: 13, track_id: "file:7:subtitle:13" }),
        subtitleTrack({ index: 14, track_id: "file:7:subtitle:14" }),
      ],
      vi.fn(),
      14,
    );

    const trackRows = screen.getAllByRole("menuitem", { name: /English/ });
    expect(trackRows).toHaveLength(2);
    expect(trackRows[0]?.textContent).not.toContain("✓");
    expect(trackRows[1]?.textContent).toContain("✓");
  });

  it("distinguishes same-language tracks by badging only the default one", () => {
    renderMenu([
      subtitleTrack({
        index: 3,
        language: "de",
        label: "German",
        track_id: "file:7:subtitle:3",
        url: "/stream/session-1/subtitles/0.srt?embedded_stream_index=3",
        default: true,
      }),
      subtitleTrack({
        index: 4,
        language: "de",
        label: "German",
        track_id: "file:7:subtitle:4",
        url: "/stream/session-1/subtitles/1.srt?embedded_stream_index=4",
      }),
      subtitleTrack({
        index: 5,
        language: "en",
        label: "English",
        track_id: "file:7:subtitle:5",
        url: "/stream/session-1/subtitles/2.srt?embedded_stream_index=5",
      }),
    ]);

    const germanRows = screen.getAllByRole("menuitem", { name: /German/ });
    expect(germanRows).toHaveLength(2);
    expect(germanRows[0]?.textContent).toContain("Default");
    expect(germanRows[1]?.textContent).not.toContain("Default");
    expect(screen.getAllByText("Default")).toHaveLength(1);
    // Neither row is checked while playback has no subtitle selected.
    expect(germanRows[0]?.textContent).not.toContain("✓");
    expect(germanRows[1]?.textContent).not.toContain("✓");
  });

  it("checks only the clicked same-language row and selects its track index", () => {
    const tracks = [
      subtitleTrack({
        index: 3,
        language: "de",
        label: "German",
        track_id: "file:7:subtitle:3",
        url: "/stream/session-1/subtitles/0.srt?embedded_stream_index=3",
        default: true,
      }),
      subtitleTrack({
        index: 4,
        language: "de",
        label: "German",
        track_id: "file:7:subtitle:4",
        url: "/stream/session-1/subtitles/1.srt?embedded_stream_index=4",
      }),
    ];
    const { view, onSelect } = renderMenu(tracks, vi.fn(), 3);

    fireEvent.click(screen.getAllByRole("menuitem", { name: /German/ })[1] as HTMLElement);
    expect(onSelect).toHaveBeenCalledWith(4);

    // The parent applies the pick: row 2 alone shows the checkmark.
    fireEvent.click(screen.getByRole("button", { name: /(Enable|Disable) captions/ }));
    view.rerender(
      createElement(SubtitleMenu, {
        tracks,
        activeIndex: 4,
        onSelect,
        delayMs: 0,
        onDelayChange: () => {},
      }),
    );

    const germanRows = screen.getAllByRole("menuitem", { name: /German/ });
    expect(germanRows[0]?.textContent).not.toContain("✓");
    expect(germanRows[1]?.textContent).toContain("✓");
  });

  it("switches between same-language rows in both directions", () => {
    const tracks = [
      subtitleTrack({
        index: 3,
        language: "de",
        label: "German",
        track_id: "file:7:subtitle:3",
        url: "/stream/session-1/subtitles/0.srt?embedded_stream_index=3",
        default: true,
      }),
      subtitleTrack({
        index: 4,
        language: "de",
        label: "German",
        track_id: "file:7:subtitle:4",
        url: "/stream/session-1/subtitles/1.srt?embedded_stream_index=4",
      }),
    ];
    const onSelect = vi.fn();
    const view = render(
      createElement(SubtitleMenu, {
        tracks,
        activeIndex: 4,
        onSelect,
        delayMs: 0,
        onDelayChange: () => {},
      }),
    );

    fireEvent.click(screen.getByRole("button", { name: /(Enable|Disable) captions/ }));
    fireEvent.click(screen.getAllByRole("menuitem", { name: /German/ })[0] as HTMLElement);
    expect(onSelect).toHaveBeenLastCalledWith(3);

    view.rerender(
      createElement(SubtitleMenu, {
        tracks,
        activeIndex: 3,
        onSelect,
        delayMs: 0,
        onDelayChange: () => {},
      }),
    );
    fireEvent.click(screen.getByRole("button", { name: /(Enable|Disable) captions/ }));
    let germanRows = screen.getAllByRole("menuitem", { name: /German/ });
    expect(germanRows[0]?.textContent).toContain("✓");
    expect(germanRows[1]?.textContent).not.toContain("✓");

    fireEvent.click(germanRows[1] as HTMLElement);
    expect(onSelect).toHaveBeenLastCalledWith(4);

    view.rerender(
      createElement(SubtitleMenu, {
        tracks,
        activeIndex: 4,
        onSelect,
        delayMs: 0,
        onDelayChange: () => {},
      }),
    );
    fireEvent.click(screen.getByRole("button", { name: /(Enable|Disable) captions/ }));
    germanRows = screen.getAllByRole("menuitem", { name: /German/ });
    expect(germanRows[0]?.textContent).not.toContain("✓");
    expect(germanRows[1]?.textContent).toContain("✓");
  });

  it("locks the menu shut while the session inventory is switching", () => {
    const onSelect = vi.fn();
    const props = {
      tracks: [subtitleTrack({ index: 3 })],
      activeIndex: null,
      onSelect,
      delayMs: 0,
      onDelayChange: () => {},
    };
    const view = render(createElement(SubtitleMenu, { ...props, locked: true }));

    const trigger = screen.getByRole("button", { name: "Enable captions" });
    expect(trigger).toBeDisabled();
    expect(trigger).toHaveAttribute("aria-disabled", "true");
    fireEvent.click(trigger);
    expect(screen.queryByRole("menu")).toBeNull();
    expect(onSelect).not.toHaveBeenCalled();

    // A switch beginning mid-menu closes it rather than leave the outgoing
    // inventory selectable.
    view.rerender(createElement(SubtitleMenu, { ...props, locked: false }));
    fireEvent.click(trigger);
    expect(screen.getByRole("menu")).toBeInTheDocument();
    view.rerender(createElement(SubtitleMenu, { ...props, locked: true }));
    expect(screen.queryByRole("menu")).toBeNull();
    expect(onSelect).not.toHaveBeenCalled();
  });

  it("marks the menu unverified while the inventory is provisional", () => {
    render(
      createElement(SubtitleMenu, {
        tracks: [subtitleTrack()],
        activeIndex: null,
        onSelect: () => {},
        delayMs: 0,
        onDelayChange: () => {},
        provisional: true,
      }),
    );
    fireEvent.click(screen.getByRole("button", { name: /(Enable|Disable) captions/ }));

    expect(screen.getByText("Unverified")).toBeTruthy();
  });

  it("hides the unverified marker once the inventory is verified", () => {
    renderMenu([subtitleTrack()]);

    expect(screen.queryByText("Unverified")).toBeNull();
  });

  it("shows discovery state when subtitle tracks are empty but inventory is provisional", () => {
    render(
      createElement(SubtitleMenu, {
        tracks: [],
        activeIndex: null,
        onSelect: () => {},
        delayMs: 0,
        onDelayChange: () => {},
        mediaFileId: 42,
        provisional: true,
      }),
    );
    fireEvent.click(screen.getByRole("button", { name: /(Enable|Disable) captions/ }));

    expect(screen.getByText("Discovering subtitles…")).toBeTruthy();
    expect(screen.getByText("Unverified")).toBeTruthy();
  });

  it("shows explicit empty state when verified with no subtitles", () => {
    render(
      createElement(SubtitleMenu, {
        tracks: [],
        activeIndex: null,
        onSelect: () => {},
        delayMs: 0,
        onDelayChange: () => {},
        mediaFileId: 42,
        provisional: false,
      }),
    );
    fireEvent.click(screen.getByRole("button", { name: /(Enable|Disable) captions/ }));

    expect(screen.getByText("No subtitles available")).toBeTruthy();
    expect(screen.queryByText("Discovering subtitles…")).toBeNull();
  });

  it("shows failure state when discovery failed and subtitle tracks are empty", () => {
    render(
      createElement(SubtitleMenu, {
        tracks: [],
        activeIndex: null,
        onSelect: () => {},
        delayMs: 0,
        onDelayChange: () => {},
        mediaFileId: 42,
        provisional: true,
        failed: true,
      }),
    );
    fireEvent.click(screen.getByRole("button", { name: /(Enable|Disable) captions/ }));

    expect(screen.getByText("Subtitle discovery failed")).toBeTruthy();
    expect(screen.queryByText("Discovering subtitles…")).toBeNull();
  });

  it("shows declared subtitle rows immediately and marks them unverified", () => {
    render(
      createElement(SubtitleMenu, {
        tracks: [subtitleTrack()],
        activeIndex: null,
        onSelect: () => {},
        delayMs: 0,
        onDelayChange: () => {},
        mediaFileId: 42,
        provisional: true,
      }),
    );
    fireEvent.click(screen.getByRole("button", { name: /(Enable|Disable) captions/ }));

    expect(screen.queryByText("Discovering subtitles…")).toBeNull();
    expect(screen.getByText("English")).toBeTruthy();
    expect(screen.getByText("From release listing")).toBeTruthy();
    expect(screen.getByText("Unverified")).toBeTruthy();
  });

  it("swaps declared rows for probe evidence when it arrives", () => {
    const track = subtitleTrack();
    const view = render(
      createElement(SubtitleMenu, {
        tracks: [track],
        activeIndex: null,
        onSelect: () => {},
        delayMs: 0,
        onDelayChange: () => {},
        mediaFileId: 42,
        provisional: true,
      }),
    );
    fireEvent.click(screen.getByRole("button", { name: /(Enable|Disable) captions/ }));
    expect(screen.getByText("English")).toBeTruthy();
    expect(screen.getByText("From release listing")).toBeTruthy();

    view.rerender(
      createElement(SubtitleMenu, {
        tracks: [track],
        activeIndex: null,
        onSelect: () => {},
        delayMs: 0,
        onDelayChange: () => {},
        mediaFileId: 42,
        provisional: false,
      }),
    );

    expect(screen.getByText("English")).toBeTruthy();
    expect(screen.queryByText("From release listing")).toBeNull();
    expect(screen.queryByText("Discovering subtitles…")).toBeNull();
    expect(screen.queryByText("Unverified")).toBeNull();
  });

  it("shows failure banner even when declared tracks are present", () => {
    render(
      createElement(SubtitleMenu, {
        tracks: [subtitleTrack()],
        activeIndex: null,
        onSelect: () => {},
        delayMs: 0,
        onDelayChange: () => {},
        mediaFileId: 42,
        provisional: true,
        failed: true,
      }),
    );
    fireEvent.click(screen.getByRole("button", { name: /(Enable|Disable) captions/ }));

    expect(
      screen.getByText("Showing available subtitles — discovery did not complete"),
    ).toBeTruthy();
    expect(screen.getByText("English")).toBeTruthy();
  });

  it("does not claim discovery failed above a verified, populated list", () => {
    // Regression: a timed-out poll sets `failed` while a plan/catalog refresh
    // has already supplied the confident, correct list. The failure notice must
    // not qualify a verified list.
    render(
      createElement(SubtitleMenu, {
        tracks: [subtitleTrack()],
        activeIndex: null,
        onSelect: () => {},
        delayMs: 0,
        onDelayChange: () => {},
        mediaFileId: 42,
        provisional: false,
        failed: true,
      }),
    );
    fireEvent.click(screen.getByRole("button", { name: /(Enable|Disable) captions/ }));

    expect(
      screen.queryByText("Showing available subtitles — discovery did not complete"),
    ).toBeNull();
    expect(screen.queryByText("Subtitle discovery failed")).toBeNull();
    expect(screen.queryByText("Discovering subtitles…")).toBeNull();
    expect(screen.getByText("English")).toBeTruthy();
  });

  it("shows the empty state, not a failure, for a verified empty list with a stale failed flag", () => {
    render(
      createElement(SubtitleMenu, {
        tracks: [],
        activeIndex: null,
        onSelect: () => {},
        delayMs: 0,
        onDelayChange: () => {},
        mediaFileId: 42,
        provisional: false,
        failed: true,
      }),
    );
    fireEvent.click(screen.getByRole("button", { name: /(Enable|Disable) captions/ }));

    expect(screen.getByText("No subtitles available")).toBeTruthy();
    expect(screen.queryByText("Subtitle discovery failed")).toBeNull();
  });

  it("does not show the discovery spinner over a verified list while the poll is still owed", () => {
    // A refreshed plan can supply the confident list while the deferred poll
    // is still pending; `pending` alone must not claim discovery in that state.
    render(
      createElement(SubtitleMenu, {
        tracks: [subtitleTrack()],
        activeIndex: null,
        onSelect: () => {},
        delayMs: 0,
        onDelayChange: () => {},
        mediaFileId: 42,
        provisional: false,
        pending: true,
      }),
    );
    fireEvent.click(screen.getByRole("button", { name: /(Enable|Disable) captions/ }));

    expect(screen.queryByText("Discovering subtitles…")).toBeNull();
    expect(screen.queryByText("Unverified")).toBeNull();
    expect(screen.getByText("English")).toBeTruthy();
  });
});

describe("SubtitleMenu discovery bound", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  function renderProvisional(onRetryDiscovery?: () => void) {
    const view = render(
      createElement(SubtitleMenu, {
        tracks: [],
        activeIndex: null,
        onSelect: () => {},
        delayMs: 0,
        onDelayChange: () => {},
        mediaFileId: 42,
        provisional: true,
        onRetryDiscovery,
      }),
    );
    fireEvent.click(screen.getByRole("button", { name: /(Enable|Disable) captions/ }));
    return view;
  }

  it("replaces the unbounded spinner with an explicit retry when discovery stalls", () => {
    vi.useFakeTimers();
    renderProvisional();

    expect(screen.getByText("Discovering subtitles…")).toBeTruthy();
    act(() => {
      vi.advanceTimersByTime(PROVISIONAL_INVENTORY_TIMEOUT_MS);
    });

    expect(screen.queryByText("Discovering subtitles…")).toBeNull();
    expect(screen.getByText("Subtitles unavailable.")).toBeTruthy();
  });

  it("re-triggers discovery and shows progress again on retry", () => {
    vi.useFakeTimers();
    const onRetryDiscovery = vi.fn();
    renderProvisional(onRetryDiscovery);

    act(() => {
      vi.advanceTimersByTime(PROVISIONAL_INVENTORY_TIMEOUT_MS);
    });
    fireEvent.click(screen.getByRole("menuitem", { name: "Retry" }));

    expect(onRetryDiscovery).toHaveBeenCalledOnce();
    expect(screen.getByText("Discovering subtitles…")).toBeTruthy();
    expect(screen.queryByText("Subtitles unavailable.")).toBeNull();

    // The fresh window is itself bounded: a retry that never lands lapses
    // back to the retry action instead of spinning forever.
    act(() => {
      vi.advanceTimersByTime(PROVISIONAL_INVENTORY_TIMEOUT_MS);
    });
    expect(screen.getByText("Subtitles unavailable.")).toBeTruthy();
  });

  it("returns to live rows when the retried inventory lands", () => {
    vi.useFakeTimers();
    const props = {
      tracks: [],
      activeIndex: null,
      onSelect: () => {},
      delayMs: 0,
      onDelayChange: () => {},
      mediaFileId: 42,
      provisional: true,
    };
    const view = render(createElement(SubtitleMenu, props));
    fireEvent.click(screen.getByRole("button", { name: /(Enable|Disable) captions/ }));
    act(() => {
      vi.advanceTimersByTime(PROVISIONAL_INVENTORY_TIMEOUT_MS);
    });
    expect(screen.getByText("Subtitles unavailable.")).toBeTruthy();

    view.rerender(
      createElement(SubtitleMenu, {
        ...props,
        tracks: [subtitleTrack()],
        provisional: false,
      }),
    );

    expect(screen.getByText("English")).toBeTruthy();
    expect(screen.queryByText("Subtitles unavailable.")).toBeNull();
    expect(screen.queryByText("Discovering subtitles…")).toBeNull();
  });

  it("offers the same retry beside a terminal discovery failure", () => {
    vi.useFakeTimers();
    const onRetryDiscovery = vi.fn();
    render(
      createElement(SubtitleMenu, {
        tracks: [],
        activeIndex: null,
        onSelect: () => {},
        delayMs: 0,
        onDelayChange: () => {},
        mediaFileId: 42,
        provisional: true,
        failed: true,
        onRetryDiscovery,
      }),
    );
    fireEvent.click(screen.getByRole("button", { name: /(Enable|Disable) captions/ }));

    expect(screen.getByText("Subtitle discovery failed")).toBeTruthy();
    fireEvent.click(screen.getByRole("menuitem", { name: "Retry" }));
    expect(onRetryDiscovery).toHaveBeenCalledOnce();
  });
});
