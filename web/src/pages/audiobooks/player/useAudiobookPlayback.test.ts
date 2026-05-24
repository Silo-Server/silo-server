import { describe, expect, it, vi, beforeEach } from "vitest";
import { renderHook, act } from "@testing-library/react";
import { useAudiobookPlayback } from "./useAudiobookPlayback";
import type { AudiobookFile } from "@/lib/audiobooks/types";

vi.mock("@/hooks/audiobooks/useReportAudiobookProgress", () => ({
  useReportAudiobookProgress: () => ({ mutate: vi.fn() }),
}));
vi.mock("@/hooks/queries/downloads", () => ({
  buildDirectDownloadUrl: (id: number) => `/stream/${id}`,
}));

const files: AudiobookFile[] = [
  {
    id: 1,
    path: "a.m4b",
    duration_seconds: 600,
    chapters: [
      { index: 0, title: "One", source: "embedded", start_seconds: 0, end_seconds: 300 },
      { index: 1, title: "Two", source: "embedded", start_seconds: 300, end_seconds: 600 },
    ],
  },
];

function makeAudio() {
  const audio = document.createElement("audio");
  Object.defineProperty(audio, "duration", { value: 600, writable: true });
  Object.defineProperty(audio, "paused", { value: true, writable: true });
  audio.play = vi.fn().mockResolvedValue(undefined);
  audio.pause = vi.fn();
  return audio;
}

describe("useAudiobookPlayback", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });

  it("returns a flattened chapter list across files", () => {
    const { result } = renderHook(() =>
      useAudiobookPlayback({ contentId: "c", files, initialPositionSeconds: 0 }),
    );
    expect(result.current.chapters).toHaveLength(2);
    expect(result.current.chapters[0].start_seconds).toBe(0);
    expect(result.current.chapters[1].start_seconds).toBe(300);
  });

  it("computes streamUrl from the first file id", () => {
    const { result } = renderHook(() =>
      useAudiobookPlayback({ contentId: "c", files, initialPositionSeconds: 0 }),
    );
    expect(result.current.streamUrl).toBe("/stream/1");
  });

  it("togglePlay invokes audio.play when paused, audio.pause otherwise", () => {
    const { result } = renderHook(() =>
      useAudiobookPlayback({ contentId: "c", files, initialPositionSeconds: 0 }),
    );
    const audio = makeAudio();
    act(() => {
      (result.current.audioRef as React.MutableRefObject<HTMLAudioElement>).current = audio;
    });
    act(() => result.current.togglePlay());
    expect(audio.play).toHaveBeenCalled();
    Object.defineProperty(audio, "paused", { value: false, writable: true });
    act(() => result.current.togglePlay());
    expect(audio.pause).toHaveBeenCalled();
  });

  it("seekTo clamps to [0, duration]", () => {
    const { result } = renderHook(() =>
      useAudiobookPlayback({ contentId: "c", files, initialPositionSeconds: 0 }),
    );
    const audio = makeAudio();
    act(() => {
      (result.current.audioRef as React.MutableRefObject<HTMLAudioElement>).current = audio;
    });
    act(() => result.current.seekTo(1_000_000));
    expect(audio.currentTime).toBe(599); // 600 - 1 (clamp to duration - 1 per existing behavior)
    act(() => result.current.seekTo(-50));
    expect(audio.currentTime).toBe(0);
  });

  it("currentChapter starts at the first chapter when currentTime is 0", () => {
    const { result } = renderHook(() =>
      useAudiobookPlayback({ contentId: "c", files, initialPositionSeconds: 0 }),
    );
    expect(result.current.currentChapter?.title).toBe("One");
  });

  it("setSleep arms a duration timer that fires after the configured seconds", () => {
    const { result } = renderHook(() =>
      useAudiobookPlayback({ contentId: "c", files, initialPositionSeconds: 0 }),
    );
    const audio = makeAudio();
    Object.defineProperty(audio, "paused", { value: false, writable: true });
    act(() => {
      (result.current.audioRef as React.MutableRefObject<HTMLAudioElement>).current = audio;
    });
    act(() => result.current.setSleep({ kind: "duration", seconds: 1 }));
    expect(result.current.sleep.remainingMs).toBeGreaterThan(0);
    act(() => {
      vi.advanceTimersByTime(1500);
    });
    expect(audio.pause).toHaveBeenCalled();
  });

  it("setSleep with off clears any armed timer", () => {
    const { result } = renderHook(() =>
      useAudiobookPlayback({ contentId: "c", files, initialPositionSeconds: 0 }),
    );
    act(() => result.current.setSleep({ kind: "duration", seconds: 5 }));
    expect(result.current.sleep.remainingMs).toBeGreaterThan(0);
    act(() => result.current.setSleep({ kind: "off" }));
    expect(result.current.sleep.remainingMs).toBeNull();
  });
});
