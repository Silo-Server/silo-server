// @vitest-environment jsdom

import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from "vitest";

import type { IntroSkipMode } from "../types";
import { markerOccurrenceAtTime, resolveMarkerRegions } from "../utils/watchPageMarkers";
import { INTRO_PROMPT_SECONDS, useIntroSkipPrompt } from "./useIntroSkipPrompt";

type SeekHandler = (seconds: number) => boolean | Promise<boolean>;

interface Props {
  mode: IntroSkipMode;
  currentTime: number;
  playing: boolean;
  enabled: boolean;
  controlsVisible?: boolean;
}

describe("useIntroSkipPrompt", () => {
  const intro = { start: 10, end: 20 };
  let onSeek: Mock<SeekHandler>;

  beforeEach(() => {
    vi.useFakeTimers();
    // The default player accepts every seek; the refusal cases opt out.
    onSeek = vi.fn<SeekHandler>(() => true);
  });

  afterEach(() => {
    vi.clearAllTimers();
    vi.useRealTimers();
  });

  function renderPrompt(initial: Partial<Props> = {}) {
    const props: Props = {
      mode: "ask",
      currentTime: 0,
      playing: true,
      enabled: true,
      ...initial,
    };
    return renderHook(
      (current: Props) =>
        useIntroSkipPrompt({
          ...current,
          controlsVisible: current.controlsVisible ?? false,
          intro,
          introKey: "session:file:intro",
          onSeek,
        }),
      { initialProps: props },
    );
  }

  it("lets an ask timeout re-offer after the viewer leaves and returns", () => {
    const { result, rerender } = renderPrompt();
    rerender({ mode: "ask", currentTime: 12, playing: true, enabled: true });

    act(() => vi.advanceTimersByTime(INTRO_PROMPT_SECONDS * 1_000));
    expect(result.current.prompt).toBeNull();

    rerender({ mode: "ask", currentTime: 13, playing: true, enabled: true });
    expect(result.current.prompt).toBeNull();
    rerender({ mode: "ask", currentTime: 21, playing: true, enabled: true });
    rerender({ mode: "ask", currentTime: 11, playing: true, enabled: true });
    expect(result.current.prompt?.label).toBe("Skip Intro");
  });

  it("brings a timed-out ask back with the controls while the intro plays", () => {
    const inside = { mode: "ask" as const, currentTime: 12, playing: true, enabled: true };
    const { result, rerender } = renderPrompt();
    rerender(inside);
    act(() => vi.advanceTimersByTime(INTRO_PROMPT_SECONDS * 1_000));
    expect(result.current.prompt).toBeNull();

    // The viewer brings the controls up: the offer returns without a timer,
    // and goes away again with the controls.
    rerender({ ...inside, currentTime: 14, controlsVisible: true });
    expect(result.current.prompt?.label).toBe("Skip Intro");
    expect(result.current.prompt?.timer).toBeNull();
    act(() => vi.advanceTimersByTime(30_000));
    expect(result.current.prompt?.label).toBe("Skip Intro");
    rerender({ ...inside, currentTime: 15, controlsVisible: false });
    expect(result.current.prompt).toBeNull();

    rerender({ ...inside, currentTime: 16, controlsVisible: true });
    act(() => expect(result.current.select()).toBe(true));
    expect(onSeek).toHaveBeenCalledWith(20);
    expect(result.current.prompt).toBeNull();

    // Selected, so resolved: seeking back in offers nothing, controls or not.
    rerender({ ...inside, currentTime: 21, controlsVisible: true });
    rerender({ ...inside, currentTime: 12, controlsVisible: true });
    expect(result.current.prompt).toBeNull();
  });

  it("skips immediately in always mode and makes the pill an undo", () => {
    const { result, rerender } = renderPrompt({ mode: "always" });

    rerender({ mode: "always", currentTime: 12, playing: true, enabled: true });
    expect(onSeek).toHaveBeenCalledWith(20);
    expect(result.current.prompt?.label).toBe("Watch Intro");
    expect(result.current.prompt?.caption).toBe("Intro skipped");

    act(() => expect(result.current.select()).toBe(true));
    expect(onSeek).toHaveBeenLastCalledWith(10);

    rerender({ mode: "always", currentTime: 21, playing: true, enabled: true });
    rerender({ mode: "always", currentTime: 12, playing: true, enabled: true });
    expect(onSeek).toHaveBeenCalledTimes(2);
    expect(result.current.prompt).toBeNull();
  });

  it.each([
    { innerEnd: 30, initialTime: 12 },
    { innerEnd: 50, initialTime: 12 },
    { innerEnd: 50, initialTime: 25 },
  ])(
    "retains undo after skipping an intro containing another occurrence (%j)",
    ({ innerEnd, initialTime }) => {
      const regions = resolveMarkerRegions({
        marker_segments: [
          { kind: "intro", start_seconds: 10, end_seconds: 50 },
          { kind: "intro", start_seconds: 20, end_seconds: innerEnd },
        ],
      });
      const { result, rerender } = renderHook(
        (currentTime: number) => {
          const intro = markerOccurrenceAtTime(regions, "intro", currentTime);
          return useIntroSkipPrompt({
            intro,
            introKey: intro ? `session:file:${intro.start}:${intro.end}` : null,
            currentTime,
            mode: "always",
            playing: true,
            enabled: true,
            controlsVisible: false,
            onSeek,
          });
        },
        { initialProps: initialTime },
      );

      expect(onSeek).toHaveBeenCalledWith(50);
      rerender(50);

      expect(result.current.prompt?.label).toBe("Watch Intro");
      act(() => expect(result.current.select()).toBe(true));
      expect(onSeek).toHaveBeenLastCalledWith(10);
    },
  );

  it("does not skip again when the stream reloads before the undo expires", async () => {
    // A reanchor replan answers asynchronously, once the new plan is adopted.
    let landReanchor: ((accepted: boolean) => void) | null = null;
    onSeek.mockImplementation(
      () =>
        new Promise<boolean>((resolve) => {
          landReanchor = resolve;
        }),
    );
    const { result, rerender } = renderPrompt({ mode: "always" });
    rerender({ mode: "always", currentTime: 12, playing: true, enabled: true });
    expect(onSeek).toHaveBeenCalledTimes(1);
    expect(result.current.prompt?.label).toBe("Watch Intro");

    // The reload disables the prompt before the replan reports back, then
    // lands a little before the intro end.
    rerender({ mode: "always", currentTime: 20, playing: false, enabled: false });
    expect(result.current.prompt).toBeNull();
    await act(async () => landReanchor?.(true));
    rerender({ mode: "always", currentTime: 18, playing: true, enabled: true });
    rerender({ mode: "always", currentTime: 19, playing: true, enabled: true });

    expect(onSeek).toHaveBeenCalledTimes(1);
    expect(result.current.prompt).toBeNull();
  });

  it("retries the automatic skip when the reanchor replan is refused", async () => {
    onSeek.mockImplementation(() => Promise.resolve(false));
    const { result, rerender } = renderPrompt({ mode: "always" });
    rerender({ mode: "always", currentTime: 12, playing: true, enabled: true });
    expect(onSeek).toHaveBeenCalledTimes(1);

    // The reload disables the prompt; the refusal then lands before it
    // re-enables, leaving playback inside the intro with nothing skipped.
    rerender({ mode: "always", currentTime: 12, playing: false, enabled: false });
    await act(async () => Promise.resolve());
    expect(result.current.prompt).toBeNull();

    rerender({ mode: "always", currentTime: 12, playing: true, enabled: true });
    expect(onSeek).toHaveBeenCalledTimes(2);
  });

  // A prompt with less left than the grace window used to expire mid-pause,
  // because the countdown stayed armed while the picture was frozen.
  it("keeps a prompt with less than the grace window left alive across a pause", () => {
    const { result, rerender } = renderPrompt();
    rerender({ mode: "ask", currentTime: 12, playing: true, enabled: true });

    act(() => vi.advanceTimersByTime(9_000));
    rerender({ mode: "ask", currentTime: 12, playing: false, enabled: true });
    act(() => vi.advanceTimersByTime(10_000));

    expect(result.current.prompt?.label).toBe("Skip Intro");
    expect(result.current.prompt?.timer?.remainingMs).toBe(1_000);

    rerender({ mode: "ask", currentTime: 12, playing: true, enabled: true });
    act(() => vi.advanceTimersByTime(999));
    expect(result.current.prompt).not.toBeNull();
    act(() => vi.advanceTimersByTime(1));
    expect(result.current.prompt).toBeNull();
  });

  // A rebuffer must leave the deadline exactly where it was, so the prompt
  // still ends ten wall-clock seconds after it appeared.
  it("resumes a sub-grace rebuffer against the original deadline", () => {
    const { result, rerender } = renderPrompt();
    rerender({ mode: "ask", currentTime: 12, playing: true, enabled: true });

    act(() => vi.advanceTimersByTime(1_000));
    rerender({ mode: "ask", currentTime: 12, playing: false, enabled: true });
    act(() => vi.advanceTimersByTime(1_000));
    rerender({ mode: "ask", currentTime: 12, playing: true, enabled: true });

    act(() => vi.advanceTimersByTime(7_999));
    expect(result.current.prompt).not.toBeNull();
    act(() => vi.advanceTimersByTime(1));
    expect(result.current.prompt).toBeNull();
  });

  it("keeps the prompt when the seek it asked for was refused", () => {
    onSeek.mockReturnValue(false);
    const { result, rerender } = renderPrompt();
    rerender({ mode: "ask", currentTime: 12, playing: true, enabled: true });

    act(() => expect(result.current.select()).toBe(true));
    expect(result.current.prompt?.label).toBe("Skip Intro");

    // Unresolved as well as visible: a second press must be able to try again.
    onSeek.mockReturnValue(true);
    act(() => expect(result.current.select()).toBe(true));
    expect(result.current.prompt).toBeNull();
  });

  it("drops the undo when the automatic skip was refused", () => {
    onSeek.mockReturnValue(false);
    const { result, rerender } = renderPrompt({ mode: "always" });

    rerender({ mode: "always", currentTime: 12, playing: true, enabled: true });

    // Nothing moved, so "Intro skipped" would be a lie.
    expect(onSeek).toHaveBeenCalledWith(20);
    expect(result.current.prompt).toBeNull();
  });

  // Recap, credits and preview ranges run on this hook in `ask` mode. One case
  // per row of the recap and credits table in the intro-skip design document,
  // which every client's tests assert.
  describe("a credits range", () => {
    const credits = { start: 100, end: 160 };
    const PROMPT_MS = 10_000;

    interface SegmentProps {
      currentTime: number;
      playing?: boolean;
      controlsVisible?: boolean;
      covered?: boolean;
    }

    function renderCredits() {
      const view = renderHook(
        ({ currentTime, playing = true, controlsVisible = false, covered }: SegmentProps) =>
          useIntroSkipPrompt({
            mode: "ask",
            intro: credits,
            introKey: "session:file:credits:100:160",
            currentTime,
            playing,
            enabled: true,
            controlsVisible,
            covered,
            onSeek,
            skipLabel: "Skip Credits",
          }),
        { initialProps: { currentTime: 90 } as SegmentProps },
      );
      view.rerender({ currentTime: 101 });
      return view;
    }

    function leaveAndReturn(rerender: (props: SegmentProps) => void) {
      rerender({ currentTime: 170 });
      rerender({ currentTime: 110 });
    }

    it("shows its pill with a full timer on entering the range", () => {
      const { result } = renderCredits();
      expect(result.current.prompt?.label).toBe("Skip Credits");
      expect(result.current.prompt?.timer).toMatchObject({
        durationMs: PROMPT_MS,
        remainingMs: PROMPT_MS,
      });
    });

    it("skips to the end in one press and stays resolved", () => {
      const { result, rerender } = renderCredits();
      act(() => expect(result.current.select()).toBe(true));
      expect(onSeek).toHaveBeenCalledWith(160);
      expect(result.current.prompt).toBeNull();

      leaveAndReturn(rerender);
      expect(result.current.prompt).toBeNull();
    });

    // A reanchor moves the position to the range end before its replan
    // answers, so the pill has already gone when the skip is accepted.
    it("stays resolved when the accepted skip lands after the pill has gone", async () => {
      let landReanchor: ((accepted: boolean) => void) | null = null;
      onSeek.mockImplementation(
        () =>
          new Promise<boolean>((resolve) => {
            landReanchor = resolve;
          }),
      );
      const { result, rerender } = renderCredits();
      act(() => expect(result.current.select()).toBe(true));
      rerender({ currentTime: 160 });
      expect(result.current.prompt).toBeNull();
      await act(async () => landReanchor?.(true));

      rerender({ currentTime: 110 });
      expect(result.current.prompt).toBeNull();
    });

    it("hides unresolved when the timer runs out, giving Select back", () => {
      const { result } = renderCredits();
      act(() => vi.advanceTimersByTime(PROMPT_MS - 1));
      expect(result.current.prompt).not.toBeNull();
      act(() => vi.advanceTimersByTime(1));
      expect(result.current.prompt).toBeNull();
      expect(result.current.select()).toBe(false);
      expect(onSeek).not.toHaveBeenCalled();
    });

    it("comes back without a timer with the controls after timing out", () => {
      const { result, rerender } = renderCredits();
      act(() => vi.advanceTimersByTime(PROMPT_MS));
      rerender({ currentTime: 115, controlsVisible: true });
      expect(result.current.prompt?.label).toBe("Skip Credits");
      expect(result.current.prompt?.timer).toBeNull();
      // Back is not the returned pill's to claim.
      expect(result.current.dismiss()).toBe(false);
    });

    it("takes one Back to hide and resolve it", () => {
      const { result, rerender } = renderCredits();
      act(() => expect(result.current.dismiss()).toBe(true));
      expect(result.current.prompt).toBeNull();
      // The next Back is the player's again.
      expect(result.current.dismiss()).toBe(false);

      leaveAndReturn(rerender);
      act(() => vi.advanceTimersByTime(PROMPT_MS));
      rerender({ currentTime: 120, controlsVisible: true });
      expect(result.current.prompt).toBeNull();
      expect(onSeek).not.toHaveBeenCalled();
    });

    it("freezes its timer across a pause and resumes from there", () => {
      const { result, rerender } = renderCredits();
      act(() => vi.advanceTimersByTime(3_000));
      rerender({ currentTime: 104, playing: false });
      act(() => vi.advanceTimersByTime(60_000));
      expect(result.current.prompt?.timer).toMatchObject({ deadlineMs: null, remainingMs: 7_000 });

      rerender({ currentTime: 104 });
      act(() => vi.advanceTimersByTime(6_999));
      expect(result.current.prompt).not.toBeNull();
      act(() => vi.advanceTimersByTime(1));
      expect(result.current.prompt).toBeNull();
    });

    // The Next Up screen covering the player is not a rebuffer: a visit
    // shorter than the pause grace window must not spend the timer.
    it("freezes its timer at once while the player is covered", () => {
      const { result, rerender } = renderCredits();
      act(() => vi.advanceTimersByTime(9_000));
      rerender({ currentTime: 109, playing: false, covered: true });
      act(() => vi.advanceTimersByTime(1_200));
      expect(result.current.prompt?.timer).toMatchObject({ deadlineMs: null, remainingMs: 1_000 });

      rerender({ currentTime: 109 });
      act(() => vi.advanceTimersByTime(999));
      expect(result.current.prompt).not.toBeNull();
      act(() => vi.advanceTimersByTime(1));
      expect(result.current.prompt).toBeNull();
    });

    it("hides unresolved on a seek out, and a seek back in restarts the full timer", () => {
      const { result, rerender } = renderCredits();
      act(() => vi.advanceTimersByTime(6_000));
      rerender({ currentTime: 170 });
      expect(result.current.prompt).toBeNull();

      rerender({ currentTime: 110 });
      expect(result.current.prompt?.timer?.remainingMs).toBe(PROMPT_MS);
      act(() => vi.advanceTimersByTime(PROMPT_MS - 1));
      expect(result.current.prompt).not.toBeNull();
    });
  });
});
