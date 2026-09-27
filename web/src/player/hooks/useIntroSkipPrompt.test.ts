// @vitest-environment jsdom

import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from "vitest";

import type { IntroSkipMode } from "../types";
import { markerOccurrenceAtTime, resolveMarkerRegions } from "../utils/watchPageMarkers";
import {
  INTRO_PROMPT_SECONDS,
  PLAYBACK_PAUSE_GRACE_MS,
  useIntroSkipPrompt,
} from "./useIntroSkipPrompt";

type SeekHandler = (seconds: number) => boolean | Promise<boolean>;

interface Props {
  mode: IntroSkipMode;
  currentTime: number;
  playing: boolean;
  enabled: boolean;
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
          intro,
          introKey: "session:file:intro",
          onSeek,
        }),
      { initialProps: props },
    );
  }

  it("does nothing in never mode", () => {
    const { result, rerender } = renderPrompt({ mode: "never" });

    rerender({ mode: "never", currentTime: 12, playing: true, enabled: true });

    expect(result.current.prompt).toBeNull();
    expect(onSeek).not.toHaveBeenCalled();
  });

  it("offers ask once and resolves the intro when selected", () => {
    const { result, rerender } = renderPrompt();

    rerender({ mode: "ask", currentTime: 12, playing: true, enabled: true });
    expect(result.current.prompt?.label).toBe("Skip Intro");

    act(() => expect(result.current.select()).toBe(true));
    expect(onSeek).toHaveBeenCalledWith(20);
    expect(result.current.prompt).toBeNull();

    rerender({ mode: "ask", currentTime: 21, playing: true, enabled: true });
    rerender({ mode: "ask", currentTime: 12, playing: true, enabled: true });
    expect(result.current.prompt).toBeNull();
  });

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

  it("resolves an automatic skip when its undo times out", () => {
    const { result, rerender } = renderPrompt({ mode: "always" });
    rerender({ mode: "always", currentTime: 12, playing: true, enabled: true });

    act(() => vi.advanceTimersByTime(INTRO_PROMPT_SECONDS * 1_000));
    expect(result.current.prompt).toBeNull();

    rerender({ mode: "always", currentTime: 21, playing: true, enabled: true });
    rerender({ mode: "always", currentTime: 12, playing: true, enabled: true });
    expect(onSeek).toHaveBeenCalledTimes(1);
  });

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

  it("ignores a short false edge and freezes after the pause grace", () => {
    const { result, rerender } = renderPrompt();
    rerender({ mode: "ask", currentTime: 12, playing: true, enabled: true });

    act(() => vi.advanceTimersByTime(1_000));
    rerender({ mode: "ask", currentTime: 12, playing: false, enabled: true });
    act(() => vi.advanceTimersByTime(PLAYBACK_PAUSE_GRACE_MS));
    const pausedRemaining = result.current.prompt?.remainingMs ?? 0;

    act(() => vi.advanceTimersByTime(2_000));
    expect(result.current.prompt?.remainingMs).toBe(pausedRemaining);

    // Resuming replays exactly what was frozen — the grace window itself is
    // not deducted, so the clock has the same 4 s left it had at the edge.
    rerender({ mode: "ask", currentTime: 12, playing: true, enabled: true });
    act(() => vi.advanceTimersByTime(pausedRemaining));
    expect(result.current.prompt).toBeNull();
  });

  // The grace window is there to tell a rebuffer from a pause. Charging the
  // viewer for it would make every pause cost 1.5 s of a 5 s prompt.
  it("freezes a pause at what the clock had when playback stopped", () => {
    const { result, rerender } = renderPrompt();
    rerender({ mode: "ask", currentTime: 12, playing: true, enabled: true });

    act(() => vi.advanceTimersByTime(1_000));
    rerender({ mode: "ask", currentTime: 12, playing: false, enabled: true });
    act(() => vi.advanceTimersByTime(PLAYBACK_PAUSE_GRACE_MS));

    expect(result.current.prompt?.remainingMs).toBe(4_000);
  });

  // A prompt with less left than the grace window used to expire mid-pause,
  // because the countdown stayed armed while the picture was frozen.
  it("keeps a prompt with less than the grace window left alive across a pause", () => {
    const { result, rerender } = renderPrompt();
    rerender({ mode: "ask", currentTime: 12, playing: true, enabled: true });

    act(() => vi.advanceTimersByTime(4_000));
    rerender({ mode: "ask", currentTime: 12, playing: false, enabled: true });
    act(() => vi.advanceTimersByTime(10_000));

    expect(result.current.prompt?.label).toBe("Skip Intro");
    expect(result.current.prompt?.remainingMs).toBe(1_000);

    rerender({ mode: "ask", currentTime: 12, playing: true, enabled: true });
    act(() => vi.advanceTimersByTime(999));
    expect(result.current.prompt).not.toBeNull();
    act(() => vi.advanceTimersByTime(1));
    expect(result.current.prompt).toBeNull();
  });

  // A rebuffer must leave the deadline exactly where it was, so the prompt
  // still ends five wall-clock seconds after it appeared.
  it("resumes a sub-grace rebuffer against the original deadline", () => {
    const { result, rerender } = renderPrompt();
    rerender({ mode: "ask", currentTime: 12, playing: true, enabled: true });

    act(() => vi.advanceTimersByTime(1_000));
    rerender({ mode: "ask", currentTime: 12, playing: false, enabled: true });
    act(() => vi.advanceTimersByTime(1_000));
    rerender({ mode: "ask", currentTime: 12, playing: true, enabled: true });

    act(() => vi.advanceTimersByTime(2_999));
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

  it("consumes back by resolving without seeking", () => {
    const { result, rerender } = renderPrompt();
    rerender({ mode: "ask", currentTime: 12, playing: true, enabled: true });

    act(() => expect(result.current.dismiss()).toBe(true));
    expect(onSeek).not.toHaveBeenCalled();

    rerender({ mode: "ask", currentTime: 21, playing: true, enabled: true });
    rerender({ mode: "ask", currentTime: 12, playing: true, enabled: true });
    expect(result.current.prompt).toBeNull();
  });
});
