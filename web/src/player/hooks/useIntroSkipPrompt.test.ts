// @vitest-environment jsdom

import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { IntroSkipMode } from "../types";
import {
  INTRO_PROMPT_SECONDS,
  PLAYBACK_PAUSE_GRACE_MS,
  useIntroSkipPrompt,
} from "./useIntroSkipPrompt";

interface Props {
  mode: IntroSkipMode;
  currentTime: number;
  playing: boolean;
  enabled: boolean;
}

describe("useIntroSkipPrompt", () => {
  const intro = { start: 10, end: 20 };
  let onSeek: (seconds: number) => void;

  beforeEach(() => {
    vi.useFakeTimers();
    onSeek = vi.fn();
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
    expect(result.current.prompt?.label).toBe("Intro Skipped · Play Intro");

    act(() => expect(result.current.select()).toBe(true));
    expect(onSeek).toHaveBeenLastCalledWith(10);

    rerender({ mode: "always", currentTime: 21, playing: true, enabled: true });
    rerender({ mode: "always", currentTime: 12, playing: true, enabled: true });
    expect(onSeek).toHaveBeenCalledTimes(2);
    expect(result.current.prompt).toBeNull();
  });

  it("resolves an automatic skip when its undo times out", () => {
    const { result, rerender } = renderPrompt({ mode: "always" });
    rerender({ mode: "always", currentTime: 12, playing: true, enabled: true });

    act(() => vi.advanceTimersByTime(INTRO_PROMPT_SECONDS * 1_000));
    expect(result.current.prompt).toBeNull();

    rerender({ mode: "always", currentTime: 21, playing: true, enabled: true });
    rerender({ mode: "always", currentTime: 12, playing: true, enabled: true });
    expect(onSeek).toHaveBeenCalledTimes(1);
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

    rerender({ mode: "ask", currentTime: 12, playing: true, enabled: true });
    act(() => vi.advanceTimersByTime(2_500));
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
