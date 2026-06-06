# Audiobook UI Redesign Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Bring the audiobook detail page and player to visual + interaction parity with Silo's video player, translated for audio, per the [design spec](../specs/2026-05-24-audiobook-ui-redesign-design.md).

**Architecture:** Extract `CircleButton` and add new menu primitives (`SpeedMenu`, `SleepTimerMenu`) into `web/src/player/components/` so both players consume the same source of truth. Split today's monolithic `AudiobookPlayer.tsx` into a state hook (`useAudiobookPlayback`) plus two chrome components (`MiniBar`, `NowListening`) under `web/src/pages/audiobooks/player/`. The same `<audio>` element backs both modes — switching is a chrome swap, not a remount. Restructure the detail page to foreground progress, chapter awareness, and narrator.

**Tech Stack:** React 19, TypeScript, Tailwind, Vite, vitest + @testing-library/react, lucide-react icons. No new dependencies.

---

## File Structure

**New files:**
- `web/src/player/components/CircleButton.tsx` — extracted glass-disc button (shared by video + audiobook)
- `web/src/player/components/CircleButton.test.tsx`
- `web/src/player/components/SpeedMenu.tsx` — popover speed picker (matches `ChaptersMenu` shape)
- `web/src/player/components/SpeedMenu.test.tsx`
- `web/src/player/components/SleepTimerMenu.tsx` — popover sleep-timer picker
- `web/src/player/components/SleepTimerMenu.test.tsx`
- `web/src/pages/audiobooks/player/AudiobookPlayer.tsx` — thin shell (mode swap)
- `web/src/pages/audiobooks/player/useAudiobookPlayback.ts` — state hook
- `web/src/pages/audiobooks/player/useAudiobookPlayback.test.ts`
- `web/src/pages/audiobooks/player/MiniBar.tsx` — compact chrome
- `web/src/pages/audiobooks/player/MiniBar.test.tsx`
- `web/src/pages/audiobooks/player/NowListening.tsx` — overlay chrome
- `web/src/pages/audiobooks/player/NowListening.test.tsx`
- `web/src/pages/audiobooks/player/CoverExpandTile.tsx` — left-edge tile + view-transition anchor
- `web/src/pages/audiobooks/components/NarratorCard.tsx`
- `web/src/pages/audiobooks/components/RelatedRail.tsx`
- `web/src/pages/audiobooks/components/ChaptersSection.tsx`
- `web/src/pages/audiobooks/components/ChaptersSection.test.tsx`

**Modified files:**
- `web/src/player/components/PlayerControls.tsx` — consume new `CircleButton`
- `web/src/pages/audiobooks/AudiobookDetail.tsx` — restructured hero, chapters, narrator, related rails

**Deleted files:**
- `web/src/pages/audiobooks/AudiobookPlayer.tsx` — replaced by the `player/` subfolder

---

## Working agreements

- After each task: run `cd web && pnpm test -- --run <test-path>` then `cd web && pnpm run lint` and `cd web && pnpm run format:check` before committing. If lint/format fails, fix and re-stage.
- Commit messages follow the existing convention: `feat(audiobooks): ...` for behavior changes, `refactor(player): ...` for pure refactors, `test(audiobooks): ...` if adding only tests.
- Don't bundle unrelated work into a single commit. One task = one commit.
- This plan ships v1 only. Bookmarks, car mode, and the cross-book backend joins are deferred per the spec.

---

## Task 1: Extract `CircleButton` shared primitive

**Files:**
- Create: `web/src/player/components/CircleButton.tsx`
- Create: `web/src/player/components/CircleButton.test.tsx`
- Modify: `web/src/player/components/PlayerControls.tsx` (remove inline `CircleButton`, import the extracted one)

The video player and (current) audiobook player both define near-identical glass-disc buttons inline. Extract a single primitive both can use. Today's video `PlayerControls.tsx:380-408` has the canonical version — that's the one to extract.

- [ ] **Step 1: Write the failing test**

Create `web/src/player/components/CircleButton.test.tsx`:

```tsx
import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { CircleButton } from "./CircleButton";

describe("CircleButton", () => {
  it("calls onClick when clicked", async () => {
    const onClick = vi.fn();
    render(
      <CircleButton size="sm" variant="secondary" ariaLabel="Test" onClick={onClick}>
        x
      </CircleButton>,
    );
    await userEvent.click(screen.getByRole("button", { name: "Test" }));
    expect(onClick).toHaveBeenCalledTimes(1);
  });

  it("applies the primary skin class for variant=primary", () => {
    render(
      <CircleButton size="md" variant="primary" ariaLabel="Play">
        ▶
      </CircleButton>,
    );
    const btn = screen.getByRole("button", { name: "Play" });
    expect(btn.className).toContain("player-disc-primary");
  });

  it("sets data-paused when prop is true", () => {
    render(
      <CircleButton size="md" variant="primary" ariaLabel="Play" data-paused>
        ▶
      </CircleButton>,
    );
    expect(screen.getByRole("button")).toHaveAttribute("data-paused", "true");
  });
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd web && pnpm test -- --run src/player/components/CircleButton.test.tsx`
Expected: FAIL — `Cannot find module './CircleButton'`

- [ ] **Step 3: Create the primitive**

Create `web/src/player/components/CircleButton.tsx`:

```tsx
export type CircleButtonProps = {
  size: "sm" | "md" | "lg";
  variant: "primary" | "secondary";
  ariaLabel: string;
  onClick?: () => void;
  disabled?: boolean;
  children: React.ReactNode;
  "data-paused"?: boolean;
};

/**
 * Glass-disc button used in both the video and audiobook player transports.
 * - `primary` = glossy white disc (play/pause).
 * - `secondary` = subtle glass disc (skip, prev/next).
 * Sizes: `sm` 40–44px in-bar secondaries, `md` 52–56px in-bar play,
 * `lg` 80px for floating variants (e.g. Now Listening play button).
 */
export function CircleButton({
  size,
  variant,
  ariaLabel,
  onClick,
  disabled,
  children,
  "data-paused": dataPaused,
}: CircleButtonProps) {
  const base =
    "flex items-center justify-center rounded-full focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-white/75 disabled:opacity-40 disabled:cursor-not-allowed";
  const sizing =
    size === "lg"
      ? "h-20 w-20"
      : size === "md"
        ? "h-12 w-12 sm:h-14 sm:w-14"
        : "h-10 w-10 sm:h-11 sm:w-11";
  const skin = variant === "primary" ? "player-disc-primary" : "player-disc-secondary";
  return (
    <button
      type="button"
      aria-label={ariaLabel}
      onClick={onClick}
      disabled={disabled}
      className={`${base} ${sizing} ${skin}`}
      data-paused={dataPaused ? "true" : undefined}
    >
      {children}
    </button>
  );
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd web && pnpm test -- --run src/player/components/CircleButton.test.tsx`
Expected: PASS — 3 tests

- [ ] **Step 5: Migrate `PlayerControls.tsx` to use the extracted button**

In `web/src/player/components/PlayerControls.tsx`:
1. Add at top with other imports: `import { CircleButton } from "./CircleButton";`
2. Delete the inline `CircleButton` function (lines ~380-408) AND the now-unused `CircleButtonProps` type just above it.
3. Leave `ClusterSlotSpacer` and `SkipIcon` alone — they are not extracted.

- [ ] **Step 6: Verify nothing else broke**

Run: `cd web && pnpm test -- --run src/player`
Expected: All player tests PASS

Run: `cd web && pnpm run lint && pnpm run format:check`
Expected: clean

- [ ] **Step 7: Commit**

```bash
git add web/src/player/components/CircleButton.tsx \
        web/src/player/components/CircleButton.test.tsx \
        web/src/player/components/PlayerControls.tsx
git commit -m "refactor(player): extract CircleButton shared primitive"
```

---

## Task 2: `SpeedMenu` primitive — replace audiobook `<select>`

**Files:**
- Create: `web/src/player/components/SpeedMenu.tsx`
- Create: `web/src/player/components/SpeedMenu.test.tsx`
- Modify: `web/src/pages/audiobooks/AudiobookPlayer.tsx` (swap `<select>` for `<SpeedMenu>`)

Today the audiobook player uses a raw `<select>`. Replace it with a popover menu styled like `ChaptersMenu` so the right-rail looks polished and consistent.

- [ ] **Step 1: Write the failing test**

Create `web/src/player/components/SpeedMenu.test.tsx`:

```tsx
import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { SpeedMenu } from "./SpeedMenu";

const RATES = [0.75, 1, 1.25, 1.5, 2] as const;

describe("SpeedMenu", () => {
  it("shows the current rate label on the trigger", () => {
    render(<SpeedMenu rates={RATES} value={1.5} onChange={() => {}} />);
    expect(screen.getByRole("button", { name: /playback speed/i })).toHaveTextContent("1.5×");
  });

  it("opens, lists all rates, and emits the chosen value", async () => {
    const onChange = vi.fn();
    render(<SpeedMenu rates={RATES} value={1} onChange={onChange} />);
    await userEvent.click(screen.getByRole("button", { name: /playback speed/i }));
    expect(screen.getAllByRole("menuitem")).toHaveLength(RATES.length);
    await userEvent.click(screen.getByRole("menuitem", { name: "1.25×" }));
    expect(onChange).toHaveBeenCalledWith(1.25);
  });

  it("marks the current rate as active", async () => {
    render(<SpeedMenu rates={RATES} value={1.5} onChange={() => {}} />);
    await userEvent.click(screen.getByRole("button", { name: /playback speed/i }));
    const active = screen.getByRole("menuitem", { name: "1.5×" });
    expect(active).toHaveAttribute("data-active", "true");
  });
});
```

- [ ] **Step 2: Run test — verify it fails**

Run: `cd web && pnpm test -- --run src/player/components/SpeedMenu.test.tsx`
Expected: FAIL — `Cannot find module './SpeedMenu'`

- [ ] **Step 3: Implement the menu**

Create `web/src/player/components/SpeedMenu.tsx` (mirrors `ChaptersMenu.tsx` shape):

```tsx
import { useCallback, useEffect, useRef, useState } from "react";

interface SpeedMenuProps {
  rates: readonly number[];
  value: number;
  onChange: (rate: number) => void;
}

export function SpeedMenu({ rates, value, onChange }: SpeedMenuProps) {
  const [open, setOpen] = useState(false);
  const menuRef = useRef<HTMLDivElement>(null);
  const menuItemsRef = useRef<(HTMLButtonElement | null)[]>([]);

  const handleBlur = useCallback((e: React.FocusEvent) => {
    if (!menuRef.current?.contains(e.relatedTarget as Node)) {
      setOpen(false);
    }
  }, []);

  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && setOpen(false);
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [open]);

  const handleMenuKeyDown = useCallback((e: React.KeyboardEvent) => {
    const items = menuItemsRef.current.filter(Boolean) as HTMLButtonElement[];
    if (items.length === 0) return;
    const i = items.indexOf(document.activeElement as HTMLButtonElement);
    let next: number | null = null;
    switch (e.key) {
      case "ArrowDown":
        next = i < items.length - 1 ? i + 1 : 0;
        break;
      case "ArrowUp":
        next = i > 0 ? i - 1 : items.length - 1;
        break;
      case "Home":
        next = 0;
        break;
      case "End":
        next = items.length - 1;
        break;
      default:
        return;
    }
    e.preventDefault();
    items[next]?.focus();
  }, []);

  return (
    <div ref={menuRef} className="relative" onBlur={handleBlur}>
      <button
        type="button"
        className="player-utility-btn px-2 text-xs tabular-nums"
        onClick={() => setOpen((v) => !v)}
        aria-label="Playback speed"
        aria-expanded={open}
        aria-haspopup="menu"
      >
        {value}×
      </button>

      {open && (
        <div
          role="menu"
          className="absolute right-0 bottom-full mb-2 flex min-w-[100px] flex-col overflow-hidden rounded-lg bg-black/90 py-1.5 shadow-xl backdrop-blur-sm"
          onKeyDown={handleMenuKeyDown}
        >
          {rates.map((r, idx) => (
            <button
              key={r}
              ref={(el) => {
                menuItemsRef.current[idx] = el;
              }}
              role="menuitem"
              type="button"
              data-active={r === value ? "true" : undefined}
              className={`w-full px-4 py-2 text-right text-sm tabular-nums transition-colors hover:bg-white/10 focus-visible:ring-2 focus-visible:ring-white/70 focus-visible:outline-none ${
                r === value ? "bg-white/5 text-white" : "text-white/75"
              }`}
              onClick={() => {
                onChange(r);
                setOpen(false);
              }}
            >
              {r}×
            </button>
          ))}
        </div>
      )}
    </div>
  );
}
```

- [ ] **Step 4: Run test — verify it passes**

Run: `cd web && pnpm test -- --run src/player/components/SpeedMenu.test.tsx`
Expected: PASS — 3 tests

- [ ] **Step 5: Swap the `<select>` in `AudiobookPlayer.tsx`**

In `web/src/pages/audiobooks/AudiobookPlayer.tsx`:
1. Add import: `import { SpeedMenu } from "@/player/components/SpeedMenu";`
2. Replace the `<select>` block (currently at lines ~288-300) with:

```tsx
<SpeedMenu
  rates={PLAYBACK_RATES}
  value={rate}
  onChange={(r) => {
    setRate(r);
    if (audioRef.current) audioRef.current.playbackRate = r;
  }}
/>
```

3. Delete the unused `handleRateChange` function (now-dead code).

- [ ] **Step 6: Lint + commit**

```bash
cd web && pnpm run lint && pnpm run format:check
```

```bash
git add web/src/player/components/SpeedMenu.tsx \
        web/src/player/components/SpeedMenu.test.tsx \
        web/src/pages/audiobooks/AudiobookPlayer.tsx
git commit -m "feat(audiobooks): replace raw select with SpeedMenu popover"
```

---

## Task 3: Move `AudiobookPlayer.tsx` to `player/` subfolder

**Files:**
- Move: `web/src/pages/audiobooks/AudiobookPlayer.tsx` → `web/src/pages/audiobooks/player/AudiobookPlayer.tsx`
- Modify: `web/src/pages/audiobooks/AudiobookDetail.tsx` (update import path)

Pure file move — no behavior change. Sets up the directory for the upcoming hook + chrome split.

- [ ] **Step 1: Move the file**

```bash
mkdir -p web/src/pages/audiobooks/player
git mv web/src/pages/audiobooks/AudiobookPlayer.tsx web/src/pages/audiobooks/player/AudiobookPlayer.tsx
```

- [ ] **Step 2: Update the import in `AudiobookDetail.tsx`**

In `web/src/pages/audiobooks/AudiobookDetail.tsx`, change:
```tsx
import AudiobookPlayer from "./AudiobookPlayer";
```
to:
```tsx
import AudiobookPlayer from "./player/AudiobookPlayer";
```

- [ ] **Step 3: Verify nothing else imports the old path**

Run: `cd web && grep -r 'from "@/pages/audiobooks/AudiobookPlayer"' src && grep -r 'from "\./AudiobookPlayer"' src/pages/audiobooks`
Expected: no output (no remaining references)

Run: `cd web && pnpm test -- --run && pnpm run lint`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add web/src/pages/audiobooks/player/AudiobookPlayer.tsx \
        web/src/pages/audiobooks/AudiobookDetail.tsx
git commit -m "refactor(audiobooks): move AudiobookPlayer into player/ subfolder"
```

---

## Task 4: Extract `useAudiobookPlayback` hook

**Files:**
- Create: `web/src/pages/audiobooks/player/useAudiobookPlayback.ts`
- Create: `web/src/pages/audiobooks/player/useAudiobookPlayback.test.ts`
- Modify: `web/src/pages/audiobooks/player/AudiobookPlayer.tsx` (consume the hook; chrome layout unchanged)

Pull all audio-element wiring, progress reporting, and state out of the component into a reusable hook. The component shrinks to consuming `playback.*` everywhere it used local state.

- [ ] **Step 1: Write the failing test**

Create `web/src/pages/audiobooks/player/useAudiobookPlayback.test.ts`:

```ts
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
});
```

(Chapter-from-time recomputation is covered by the `MiniBar` test in Task 6, which exercises the memoized derivation through a rendered consumer rather than poking the hook directly.)

- [ ] **Step 2: Run test — verify it fails**

Run: `cd web && pnpm test -- --run src/pages/audiobooks/player/useAudiobookPlayback.test.ts`
Expected: FAIL — `Cannot find module './useAudiobookPlayback'`

- [ ] **Step 3: Implement the hook**

Create `web/src/pages/audiobooks/player/useAudiobookPlayback.ts`:

```ts
import { useEffect, useMemo, useRef, useState, useCallback } from "react";
import { buildDirectDownloadUrl } from "@/hooks/queries/downloads";
import { useReportAudiobookProgress } from "@/hooks/audiobooks/useReportAudiobookProgress";
import type { AudiobookFile } from "@/lib/audiobooks/types";
import type { PlayerChapter } from "@/player/types";

const REPORT_INTERVAL_MS = 10_000;

export interface UseAudiobookPlaybackOptions {
  contentId: string;
  files: AudiobookFile[];
  initialPositionSeconds: number;
  autoPlay?: boolean;
}

export interface AudiobookPlayback {
  audioRef: React.RefObject<HTMLAudioElement | null>;
  streamUrl: string;
  hasFile: boolean;
  playing: boolean;
  currentTime: number;
  duration: number;
  buffered: TimeRanges | null;
  rate: number;
  chapters: PlayerChapter[];
  currentChapter: PlayerChapter | null;
  togglePlay: () => void;
  seekTo: (seconds: number) => void;
  skip: (delta: number) => void;
  setRate: (r: number) => void;
}

function safeNumber(value: number): number {
  return Number.isFinite(value) && value >= 0 ? value : 0;
}

function buildPlayerChapters(files: AudiobookFile[]): PlayerChapter[] {
  const out: PlayerChapter[] = [];
  let offset = 0;
  let nextIndex = 0;
  for (const file of files) {
    if (file.chapters) {
      for (const ch of file.chapters) {
        out.push({
          index: nextIndex++,
          title: ch.title || `Chapter ${ch.index + 1}`,
          start_seconds: offset + ch.start_seconds,
          end_seconds: offset + (ch.end_seconds || ch.start_seconds),
          source: ch.source || "embedded",
        });
      }
    }
    offset += file.duration_seconds ?? 0;
  }
  return out;
}

export function useAudiobookPlayback({
  contentId,
  files,
  initialPositionSeconds,
  autoPlay = true,
}: UseAudiobookPlaybackOptions): AudiobookPlayback {
  const audioRef = useRef<HTMLAudioElement>(null);
  const [playing, setPlaying] = useState(false);
  const [currentTime, setCurrentTime] = useState(0);
  const [duration, setDuration] = useState(0);
  const [buffered, setBuffered] = useState<TimeRanges | null>(null);
  const [rate, setRateState] = useState(1);

  const reportProgress = useReportAudiobookProgress();
  const file = files[0];
  const fileId = file?.id;
  const streamUrl = fileId ? buildDirectDownloadUrl(fileId) : "";
  const chapters = useMemo(() => buildPlayerChapters(files), [files]);

  const reportRef = useRef<(pos: number) => void>(() => {});
  reportRef.current = (posSeconds: number) => {
    if (!fileId) return;
    reportProgress.mutate({
      contentId,
      positionSeconds: Math.floor(posSeconds),
      mediaFileId: fileId,
    });
  };

  useEffect(() => {
    const audio = audioRef.current;
    if (!audio || !fileId) return;

    const onTimeUpdate = () => setCurrentTime(safeNumber(audio.currentTime));
    const onProgress = () => setBuffered(audio.buffered);
    const onDurationChange = () => setDuration(safeNumber(audio.duration));
    const onLoadedMetadata = () => {
      setDuration(safeNumber(audio.duration));
      if (initialPositionSeconds > 0 && Number.isFinite(audio.duration)) {
        const target = Math.min(initialPositionSeconds, audio.duration - 1);
        if (target > 0) audio.currentTime = target;
      }
      if (autoPlay) {
        audio.play().catch((err) => {
          console.warn("audiobook autoplay blocked", err);
        });
      }
    };
    const onPlay = () => setPlaying(true);
    const onPause = () => {
      setPlaying(false);
      reportRef.current(audio.currentTime);
    };
    const onSeeked = () => {
      setCurrentTime(safeNumber(audio.currentTime));
      reportRef.current(audio.currentTime);
    };
    const onEnded = () => {
      setPlaying(false);
      reportRef.current(audio.currentTime);
    };
    const onError = () => {
      const err = audio.error;
      console.error("audiobook audio error", {
        code: err?.code,
        message: err?.message,
        networkState: audio.networkState,
        readyState: audio.readyState,
        src: audio.currentSrc,
      });
    };

    audio.addEventListener("timeupdate", onTimeUpdate);
    audio.addEventListener("progress", onProgress);
    audio.addEventListener("durationchange", onDurationChange);
    audio.addEventListener("loadedmetadata", onLoadedMetadata);
    audio.addEventListener("play", onPlay);
    audio.addEventListener("pause", onPause);
    audio.addEventListener("seeked", onSeeked);
    audio.addEventListener("ended", onEnded);
    audio.addEventListener("error", onError);

    return () => {
      audio.removeEventListener("timeupdate", onTimeUpdate);
      audio.removeEventListener("progress", onProgress);
      audio.removeEventListener("durationchange", onDurationChange);
      audio.removeEventListener("loadedmetadata", onLoadedMetadata);
      audio.removeEventListener("play", onPlay);
      audio.removeEventListener("pause", onPause);
      audio.removeEventListener("seeked", onSeeked);
      audio.removeEventListener("ended", onEnded);
      audio.removeEventListener("error", onError);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [fileId]);

  useEffect(() => {
    if (!playing) return;
    const id = window.setInterval(() => {
      const audio = audioRef.current;
      if (audio) reportRef.current(audio.currentTime);
    }, REPORT_INTERVAL_MS);
    return () => window.clearInterval(id);
  }, [playing]);

  useEffect(() => {
    return () => {
      const audio = audioRef.current;
      if (audio && !audio.paused) {
        audio.pause();
        reportRef.current(audio.currentTime);
      }
    };
  }, []);

  const togglePlay = useCallback(() => {
    const audio = audioRef.current;
    if (!audio) return;
    if (audio.paused) {
      audio.play().catch((err) => console.error("audiobook play failed", err));
    } else {
      audio.pause();
    }
  }, []);

  const seekTo = useCallback((seconds: number) => {
    const audio = audioRef.current;
    if (!audio) return;
    const max = Number.isFinite(audio.duration) ? audio.duration - 1 : seconds;
    const clamped = Math.max(0, Math.min(seconds, max));
    audio.currentTime = clamped;
    setCurrentTime(safeNumber(clamped));
  }, []);

  const skip = useCallback(
    (delta: number) => {
      const audio = audioRef.current;
      if (!audio) return;
      seekTo(audio.currentTime + delta);
    },
    [seekTo],
  );

  const setRate = useCallback((r: number) => {
    setRateState(r);
    if (audioRef.current) audioRef.current.playbackRate = r;
  }, []);

  const currentChapter = useMemo(() => {
    if (chapters.length === 0) return null;
    return (
      chapters.find((c) => currentTime >= c.start_seconds && currentTime < c.end_seconds) ?? null
    );
  }, [chapters, currentTime]);

  return {
    audioRef,
    streamUrl,
    hasFile: Boolean(file),
    playing,
    currentTime,
    duration,
    buffered,
    rate,
    chapters,
    currentChapter,
    togglePlay,
    seekTo,
    skip,
    setRate,
  };
}
```

- [ ] **Step 4: Run test — verify it passes**

Run: `cd web && pnpm test -- --run src/pages/audiobooks/player/useAudiobookPlayback.test.ts`
Expected: PASS

- [ ] **Step 5: Reshape `AudiobookPlayer.tsx` to consume the hook**

Replace `web/src/pages/audiobooks/player/AudiobookPlayer.tsx` with the version below. The chrome layout is unchanged from today — only the wiring source moves. (Task 5 will pull the chrome into a separate `MiniBar.tsx` file; here we just delegate state.)

```tsx
import { X, Pause, Play, RotateCcw, RotateCw } from "lucide-react";
import type { AudiobookFile } from "@/lib/audiobooks/types";
import { SeekBar, formatTime } from "@/player/components/SeekBar";
import { ChaptersMenu } from "@/player/components/ChaptersMenu";
import { CircleButton } from "@/player/components/CircleButton";
import { SpeedMenu } from "@/player/components/SpeedMenu";
import { useAudiobookPlayback } from "./useAudiobookPlayback";

export interface AudiobookPlayerProps {
  contentId: string;
  title?: string;
  files: AudiobookFile[];
  initialPositionSeconds?: number;
  autoPlay?: boolean;
  onClose?: () => void;
}

const SKIP_BACK_SECONDS = 30;
const SKIP_FORWARD_SECONDS = 30;
const PLAYBACK_RATES = [0.75, 1, 1.25, 1.5, 2] as const;

export default function AudiobookPlayer({
  contentId,
  title,
  files,
  initialPositionSeconds = 0,
  autoPlay = true,
  onClose,
}: AudiobookPlayerProps) {
  const playback = useAudiobookPlayback({
    contentId,
    files,
    initialPositionSeconds,
    autoPlay,
  });

  return (
    <div className="bg-background border-b px-3 pt-2 pb-2 sm:px-6">
      {playback.hasFile && (
        <audio
          ref={playback.audioRef}
          src={playback.streamUrl}
          preload="metadata"
          style={{ display: "none" }}
        />
      )}

      <SeekBar
        currentTime={playback.currentTime}
        duration={playback.duration}
        buffered={playback.buffered}
        chapters={playback.chapters}
        introRegion={null}
        creditsRegion={null}
        onSeek={playback.seekTo}
      />

      <div className="mt-1 grid grid-cols-[minmax(0,1fr)_auto_minmax(0,1fr)] items-center gap-3 sm:gap-5">
        <div className="flex min-w-0 flex-col gap-0.5">
          {title ? (
            <div
              className="truncate text-[14px] leading-tight font-semibold tracking-tight sm:text-[15px]"
              title={title}
            >
              {title}
            </div>
          ) : null}
          <div className="text-muted-foreground flex items-center gap-2 text-[10px] uppercase leading-tight">
            <span className="font-mono text-[11px] tracking-[0.12em] normal-case tabular-nums">
              {formatTime(playback.currentTime)}
              <span className="mx-1 opacity-50">/</span>
              {formatTime(playback.duration)}
            </span>
          </div>
        </div>

        <div className="flex items-center justify-center gap-2 sm:gap-3">
          <CircleButton
            size="sm"
            variant="secondary"
            ariaLabel={`Back ${SKIP_BACK_SECONDS} seconds`}
            onClick={() => playback.skip(-SKIP_BACK_SECONDS)}
            disabled={!playback.hasFile}
          >
            <SkipIcon direction="back" seconds={SKIP_BACK_SECONDS} />
          </CircleButton>

          <CircleButton
            size="md"
            variant="primary"
            ariaLabel={playback.playing ? "Pause" : "Play"}
            onClick={playback.togglePlay}
            disabled={!playback.hasFile}
            data-paused={!playback.playing}
          >
            {playback.playing ? (
              <Pause className="h-5 w-5" strokeWidth={0} fill="currentColor" />
            ) : (
              <Play className="ml-[2px] h-5 w-5" strokeWidth={0} fill="currentColor" />
            )}
          </CircleButton>

          <CircleButton
            size="sm"
            variant="secondary"
            ariaLabel={`Forward ${SKIP_FORWARD_SECONDS} seconds`}
            onClick={() => playback.skip(SKIP_FORWARD_SECONDS)}
            disabled={!playback.hasFile}
          >
            <SkipIcon direction="forward" seconds={SKIP_FORWARD_SECONDS} />
          </CircleButton>
        </div>

        <div className="flex items-center justify-end gap-2">
          {playback.chapters.length > 0 && (
            <ChaptersMenu
              chapters={playback.chapters}
              currentTime={playback.currentTime}
              onSeek={playback.seekTo}
            />
          )}
          <SpeedMenu rates={PLAYBACK_RATES} value={playback.rate} onChange={playback.setRate} />
          {onClose && (
            <button
              type="button"
              onClick={onClose}
              aria-label="Close player"
              className="text-muted-foreground hover:text-foreground rounded p-1.5"
            >
              <X className="h-4 w-4" />
            </button>
          )}
        </div>
      </div>
    </div>
  );
}

function SkipIcon({ direction, seconds }: { direction: "back" | "forward"; seconds: number }) {
  const Arrow = direction === "back" ? RotateCcw : RotateCw;
  return (
    <span className="relative flex h-6 w-6 items-center justify-center">
      <Arrow className="h-6 w-6" strokeWidth={1.6} />
      <span className="absolute inset-0 flex items-center justify-center pb-[1px] text-[8px] font-semibold tracking-tight tabular-nums">
        {seconds}
      </span>
    </span>
  );
}
```

- [ ] **Step 6: Verify visually**

Run the dev server: `cd web && pnpm dev` (with `VITE_API_PROXY_TARGET` pointing at your backend).
Open an audiobook detail page, click Play, confirm the bottom bar appears and:
- Plays/pauses on the center button
- Skip ±30 works
- Chapter menu opens and seeks
- Speed menu now shows as a popover (not a native select) and changes playback rate
- Close button still dismisses

- [ ] **Step 7: Lint + commit**

```bash
cd web && pnpm run lint && pnpm run format:check
```

```bash
git add web/src/pages/audiobooks/player/useAudiobookPlayback.ts \
        web/src/pages/audiobooks/player/useAudiobookPlayback.test.ts \
        web/src/pages/audiobooks/player/AudiobookPlayer.tsx
git commit -m "refactor(audiobooks): extract useAudiobookPlayback hook"
```

---

## Task 5: Split chrome into `MiniBar.tsx`

**Files:**
- Create: `web/src/pages/audiobooks/player/MiniBar.tsx`
- Create: `web/src/pages/audiobooks/player/MiniBar.test.tsx`
- Modify: `web/src/pages/audiobooks/player/AudiobookPlayer.tsx` (becomes a thin shell that renders `<MiniBar>` only — Now Listening swap arrives in Task 9)

Move all the JSX from `AudiobookPlayer.tsx` into `MiniBar.tsx` and have the player render it, plumbing `playback` as a single prop. No behavior change; sets up Task 9's mode swap.

- [ ] **Step 1: Write the failing test**

Create `web/src/pages/audiobooks/player/MiniBar.test.tsx`:

```tsx
import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MiniBar } from "./MiniBar";
import type { AudiobookPlayback } from "./useAudiobookPlayback";

function makePlayback(over: Partial<AudiobookPlayback> = {}): AudiobookPlayback {
  return {
    audioRef: { current: null },
    streamUrl: "",
    hasFile: true,
    playing: false,
    currentTime: 0,
    duration: 600,
    buffered: null,
    rate: 1,
    chapters: [],
    currentChapter: null,
    togglePlay: vi.fn(),
    seekTo: vi.fn(),
    skip: vi.fn(),
    setRate: vi.fn(),
    ...over,
  };
}

describe("MiniBar", () => {
  it("renders the title", () => {
    render(<MiniBar title="Project Hail Mary" playback={makePlayback()} />);
    expect(screen.getByText("Project Hail Mary")).toBeInTheDocument();
  });

  it("calls togglePlay when the center button is clicked", async () => {
    const togglePlay = vi.fn();
    render(<MiniBar title="X" playback={makePlayback({ togglePlay })} />);
    await userEvent.click(screen.getByRole("button", { name: /play|pause/i }));
    expect(togglePlay).toHaveBeenCalled();
  });

  it("calls onClose when the close button is clicked", async () => {
    const onClose = vi.fn();
    render(<MiniBar title="X" playback={makePlayback()} onClose={onClose} />);
    await userEvent.click(screen.getByRole("button", { name: /close player/i }));
    expect(onClose).toHaveBeenCalled();
  });
});
```

- [ ] **Step 2: Run test — verify it fails**

Run: `cd web && pnpm test -- --run src/pages/audiobooks/player/MiniBar.test.tsx`
Expected: FAIL — `Cannot find module './MiniBar'`

- [ ] **Step 3: Create `MiniBar.tsx`**

Create `web/src/pages/audiobooks/player/MiniBar.tsx` and move the JSX out of `AudiobookPlayer.tsx`:

```tsx
import { X, Pause, Play, RotateCcw, RotateCw } from "lucide-react";
import { SeekBar, formatTime } from "@/player/components/SeekBar";
import { ChaptersMenu } from "@/player/components/ChaptersMenu";
import { CircleButton } from "@/player/components/CircleButton";
import { SpeedMenu } from "@/player/components/SpeedMenu";
import type { AudiobookPlayback } from "./useAudiobookPlayback";

const SKIP_BACK_SECONDS = 30;
const SKIP_FORWARD_SECONDS = 30;
const PLAYBACK_RATES = [0.75, 1, 1.25, 1.5, 2] as const;

interface MiniBarProps {
  title?: string;
  playback: AudiobookPlayback;
  onClose?: () => void;
}

export function MiniBar({ title, playback, onClose }: MiniBarProps) {
  return (
    <div className="bg-background border-b px-3 pt-2 pb-2 sm:px-6">
      <SeekBar
        currentTime={playback.currentTime}
        duration={playback.duration}
        buffered={playback.buffered}
        chapters={playback.chapters}
        introRegion={null}
        creditsRegion={null}
        onSeek={playback.seekTo}
      />

      <div className="mt-1 grid grid-cols-[minmax(0,1fr)_auto_minmax(0,1fr)] items-center gap-3 sm:gap-5">
        <div className="flex min-w-0 flex-col gap-0.5">
          {title ? (
            <div
              className="truncate text-[14px] leading-tight font-semibold tracking-tight sm:text-[15px]"
              title={title}
            >
              {title}
            </div>
          ) : null}
          <div className="text-muted-foreground flex items-center gap-2 text-[10px] uppercase leading-tight">
            <span className="font-mono text-[11px] tracking-[0.12em] normal-case tabular-nums">
              {formatTime(playback.currentTime)}
              <span className="mx-1 opacity-50">/</span>
              {formatTime(playback.duration)}
            </span>
          </div>
        </div>

        <div className="flex items-center justify-center gap-2 sm:gap-3">
          <CircleButton
            size="sm"
            variant="secondary"
            ariaLabel={`Back ${SKIP_BACK_SECONDS} seconds`}
            onClick={() => playback.skip(-SKIP_BACK_SECONDS)}
            disabled={!playback.hasFile}
          >
            <SkipIcon direction="back" seconds={SKIP_BACK_SECONDS} />
          </CircleButton>

          <CircleButton
            size="md"
            variant="primary"
            ariaLabel={playback.playing ? "Pause" : "Play"}
            onClick={playback.togglePlay}
            disabled={!playback.hasFile}
            data-paused={!playback.playing}
          >
            {playback.playing ? (
              <Pause className="h-5 w-5" strokeWidth={0} fill="currentColor" />
            ) : (
              <Play className="ml-[2px] h-5 w-5" strokeWidth={0} fill="currentColor" />
            )}
          </CircleButton>

          <CircleButton
            size="sm"
            variant="secondary"
            ariaLabel={`Forward ${SKIP_FORWARD_SECONDS} seconds`}
            onClick={() => playback.skip(SKIP_FORWARD_SECONDS)}
            disabled={!playback.hasFile}
          >
            <SkipIcon direction="forward" seconds={SKIP_FORWARD_SECONDS} />
          </CircleButton>
        </div>

        <div className="flex items-center justify-end gap-2">
          {playback.chapters.length > 0 && (
            <ChaptersMenu
              chapters={playback.chapters}
              currentTime={playback.currentTime}
              onSeek={playback.seekTo}
            />
          )}
          <SpeedMenu rates={PLAYBACK_RATES} value={playback.rate} onChange={playback.setRate} />
          {onClose && (
            <button
              type="button"
              onClick={onClose}
              aria-label="Close player"
              className="text-muted-foreground hover:text-foreground rounded p-1.5"
            >
              <X className="h-4 w-4" />
            </button>
          )}
        </div>
      </div>
    </div>
  );
}

function SkipIcon({ direction, seconds }: { direction: "back" | "forward"; seconds: number }) {
  const Arrow = direction === "back" ? RotateCcw : RotateCw;
  return (
    <span className="relative flex h-6 w-6 items-center justify-center">
      <Arrow className="h-6 w-6" strokeWidth={1.6} />
      <span className="absolute inset-0 flex items-center justify-center pb-[1px] text-[8px] font-semibold tracking-tight tabular-nums">
        {seconds}
      </span>
    </span>
  );
}
```

- [ ] **Step 4: Reduce `AudiobookPlayer.tsx` to a shell**

Replace `web/src/pages/audiobooks/player/AudiobookPlayer.tsx` with:

```tsx
import type { AudiobookFile } from "@/lib/audiobooks/types";
import { useAudiobookPlayback } from "./useAudiobookPlayback";
import { MiniBar } from "./MiniBar";

export interface AudiobookPlayerProps {
  contentId: string;
  title?: string;
  files: AudiobookFile[];
  initialPositionSeconds?: number;
  autoPlay?: boolean;
  onClose?: () => void;
}

export default function AudiobookPlayer({
  contentId,
  title,
  files,
  initialPositionSeconds = 0,
  autoPlay = true,
  onClose,
}: AudiobookPlayerProps) {
  const playback = useAudiobookPlayback({
    contentId,
    files,
    initialPositionSeconds,
    autoPlay,
  });

  return (
    <>
      {playback.hasFile && (
        <audio
          ref={playback.audioRef}
          src={playback.streamUrl}
          preload="metadata"
          style={{ display: "none" }}
        />
      )}
      <MiniBar title={title} playback={playback} onClose={onClose} />
    </>
  );
}
```

- [ ] **Step 5: Run tests + lint**

Run: `cd web && pnpm test -- --run src/pages/audiobooks/player`
Expected: PASS

Run: `cd web && pnpm run lint && pnpm run format:check`
Expected: clean

- [ ] **Step 6: Commit**

```bash
git add web/src/pages/audiobooks/player/MiniBar.tsx \
        web/src/pages/audiobooks/player/MiniBar.test.tsx \
        web/src/pages/audiobooks/player/AudiobookPlayer.tsx
git commit -m "refactor(audiobooks): split MiniBar out of AudiobookPlayer shell"
```

---

## Task 6: Add chapter title to the MiniBar left column

**Files:**
- Modify: `web/src/pages/audiobooks/player/MiniBar.tsx`
- Modify: `web/src/pages/audiobooks/player/MiniBar.test.tsx`

The single most useful "where am I" cue today is missing from the bar. Pull `playback.currentChapter` and render its title between the book title and the time row.

- [ ] **Step 1: Extend the test**

Add to `web/src/pages/audiobooks/player/MiniBar.test.tsx`:

```tsx
it("renders the current chapter title under the book title", () => {
  render(
    <MiniBar
      title="X"
      playback={makePlayback({
        currentChapter: {
          index: 6,
          title: "The Astrophage",
          start_seconds: 0,
          end_seconds: 100,
          source: "embedded",
        },
      })}
    />,
  );
  expect(screen.getByText("The Astrophage")).toBeInTheDocument();
});

it("omits the chapter row when no current chapter is known", () => {
  render(<MiniBar title="X" playback={makePlayback({ currentChapter: null })} />);
  // chapter test id should not be present
  expect(screen.queryByTestId("minibar-chapter-title")).not.toBeInTheDocument();
});
```

- [ ] **Step 2: Run test — verify it fails**

Run: `cd web && pnpm test -- --run src/pages/audiobooks/player/MiniBar.test.tsx`
Expected: FAIL — the chapter title isn't rendered yet

- [ ] **Step 3: Update `MiniBar.tsx`**

In the left column of `MiniBar.tsx`, between the title `<div>` and the time row, add:

```tsx
{playback.currentChapter ? (
  <div
    data-testid="minibar-chapter-title"
    className="text-muted-foreground truncate text-[11px] leading-tight"
    title={playback.currentChapter.title}
  >
    {playback.currentChapter.title}
  </div>
) : null}
```

- [ ] **Step 4: Run tests + lint**

Run: `cd web && pnpm test -- --run src/pages/audiobooks/player/MiniBar.test.tsx`
Expected: PASS (5 tests total now)

Run: `cd web && pnpm run lint && pnpm run format:check`
Expected: clean

- [ ] **Step 5: Commit**

```bash
git add web/src/pages/audiobooks/player/MiniBar.tsx \
        web/src/pages/audiobooks/player/MiniBar.test.tsx
git commit -m "feat(audiobooks): show current chapter in MiniBar left column"
```

---

## Task 7: Add `CoverExpandTile` (no expand wiring yet)

**Files:**
- Create: `web/src/pages/audiobooks/player/CoverExpandTile.tsx`
- Modify: `web/src/pages/audiobooks/player/MiniBar.tsx` (insert tile, accept `posterUrl` + `onExpand` props)
- Modify: `web/src/pages/audiobooks/player/AudiobookPlayer.tsx` (accept and pass through `posterUrl`)
- Modify: `web/src/pages/audiobooks/AudiobookDetail.tsx` (pass `posterUrl` to player)
- Modify: `web/src/pages/audiobooks/player/MiniBar.test.tsx`

The tile lives at the far left of the bar. Clicking it fires `onExpand` (which is a no-op for now — Task 9 wires it to actually open Now Listening). Sets up the visual anchor for the mini ↔ Now Listening view transition that comes for free in Task 9 (both elements use `viewTransitionName: audiobook-cover-${contentId}`).

> **Out of scope for v1:** the spec mentions extending the same view-transition name to the detail page's hero cover so the *initial* open animates from hero → mini bar. That would require adding a `posterViewTransitionName` prop to the shared `DetailHero` component, which is consumed by movie + show pages too. Defer the cross-component change. v1 ships with the mini ↔ Now Listening transition only.

- [ ] **Step 1: Create the tile**

Create `web/src/pages/audiobooks/player/CoverExpandTile.tsx`:

```tsx
import { Maximize2 } from "lucide-react";

interface CoverExpandTileProps {
  contentId: string;
  posterUrl?: string;
  title?: string;
  onExpand?: () => void;
}

export function CoverExpandTile({ contentId, posterUrl, title, onExpand }: CoverExpandTileProps) {
  return (
    <button
      type="button"
      onClick={onExpand}
      aria-label="Open Now Listening"
      className="group/tile bg-muted relative h-[54px] w-[36px] shrink-0 overflow-hidden rounded-md transition-transform hover:scale-[1.04] focus-visible:scale-[1.04] focus-visible:outline-none"
      style={{ viewTransitionName: `audiobook-cover-${contentId}` }}
    >
      {posterUrl ? (
        <img src={posterUrl} alt={title ?? ""} className="h-full w-full object-cover" />
      ) : null}
      <span className="pointer-events-none absolute inset-0 flex items-center justify-center bg-black/40 opacity-0 transition-opacity group-hover/tile:opacity-100 group-focus-visible/tile:opacity-100">
        <Maximize2 className="h-3.5 w-3.5 text-white" />
      </span>
    </button>
  );
}
```

- [ ] **Step 2: Extend the MiniBar test**

In `web/src/pages/audiobooks/player/MiniBar.test.tsx`, add `contentId` + `posterUrl` to the helper props and add a test:

```tsx
it("invokes onExpand when the cover tile is clicked", async () => {
  const onExpand = vi.fn();
  render(
    <MiniBar
      contentId="book-1"
      title="X"
      posterUrl="/p.jpg"
      playback={makePlayback()}
      onExpand={onExpand}
    />,
  );
  await userEvent.click(screen.getByRole("button", { name: /open now listening/i }));
  expect(onExpand).toHaveBeenCalled();
});
```

Update the other tests in this file to pass `contentId="book-1"` to `MiniBar`.

- [ ] **Step 3: Update `MiniBar.tsx`**

Update the `MiniBarProps` and left column. Replace the `MiniBarProps` interface and the left column with:

```tsx
import { CoverExpandTile } from "./CoverExpandTile";

interface MiniBarProps {
  contentId: string;
  title?: string;
  posterUrl?: string;
  playback: AudiobookPlayback;
  onClose?: () => void;
  onExpand?: () => void;
}

export function MiniBar({
  contentId,
  title,
  posterUrl,
  playback,
  onClose,
  onExpand,
}: MiniBarProps) {
  // ... SeekBar unchanged
  // grid row, replace the left cell with:
  <div className="flex min-w-0 items-center gap-3">
    <CoverExpandTile
      contentId={contentId}
      posterUrl={posterUrl}
      title={title}
      onExpand={onExpand}
    />
    <div className="flex min-w-0 flex-col gap-0.5">
      {title ? (
        <div
          className="truncate text-[14px] leading-tight font-semibold tracking-tight sm:text-[15px]"
          title={title}
        >
          {title}
        </div>
      ) : null}
      {playback.currentChapter ? (
        <div
          data-testid="minibar-chapter-title"
          className="text-muted-foreground truncate text-[11px] leading-tight"
          title={playback.currentChapter.title}
        >
          {playback.currentChapter.title}
        </div>
      ) : null}
      <div className="text-muted-foreground flex items-center gap-2 text-[10px] uppercase leading-tight">
        <span className="font-mono text-[11px] tracking-[0.12em] normal-case tabular-nums">
          {formatTime(playback.currentTime)}
          <span className="mx-1 opacity-50">/</span>
          {formatTime(playback.duration)}
        </span>
      </div>
    </div>
  </div>
```

(The outer `<div className="mt-1 grid ...">` stays; only the contents of the first grid column changes.)

- [ ] **Step 4: Plumb `posterUrl` and `onExpand` through `AudiobookPlayer.tsx`**

Update `AudiobookPlayerProps` to add `posterUrl?: string;` and `onExpand?: () => void;`. Update the JSX:

```tsx
<MiniBar
  contentId={contentId}
  title={title}
  posterUrl={posterUrl}
  playback={playback}
  onClose={onClose}
  onExpand={onExpand}
/>
```

- [ ] **Step 5: Pass `posterUrl` from `AudiobookDetail.tsx`**

In `web/src/pages/audiobooks/AudiobookDetail.tsx`, in the `<AudiobookPlayer>` element add:

```tsx
posterUrl={audiobook.poster_url}
```

- [ ] **Step 6: Run tests + lint**

Run: `cd web && pnpm test -- --run src/pages/audiobooks/player`
Expected: PASS

Run: `cd web && pnpm run lint && pnpm run format:check`
Expected: clean

- [ ] **Step 7: Commit**

```bash
git add web/src/pages/audiobooks/player/CoverExpandTile.tsx \
        web/src/pages/audiobooks/player/MiniBar.tsx \
        web/src/pages/audiobooks/player/MiniBar.test.tsx \
        web/src/pages/audiobooks/player/AudiobookPlayer.tsx \
        web/src/pages/audiobooks/AudiobookDetail.tsx
git commit -m "feat(audiobooks): add CoverExpandTile to MiniBar (no expand wiring yet)"
```

---

## Task 8: Build `NowListening` overlay (not yet reachable)

**Files:**
- Create: `web/src/pages/audiobooks/player/NowListening.tsx`
- Create: `web/src/pages/audiobooks/player/NowListening.test.tsx`

Build the full-overlay chrome as a standalone component. Task 9 wires it to actually appear.

- [ ] **Step 1: Write the failing test**

Create `web/src/pages/audiobooks/player/NowListening.test.tsx`:

```tsx
import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { NowListening } from "./NowListening";
import type { AudiobookPlayback } from "./useAudiobookPlayback";

function makePlayback(over: Partial<AudiobookPlayback> = {}): AudiobookPlayback {
  return {
    audioRef: { current: null },
    streamUrl: "",
    hasFile: true,
    playing: false,
    currentTime: 0,
    duration: 600,
    buffered: null,
    rate: 1,
    chapters: [],
    currentChapter: null,
    togglePlay: vi.fn(),
    seekTo: vi.fn(),
    skip: vi.fn(),
    setRate: vi.fn(),
    ...over,
  };
}

describe("NowListening", () => {
  it("renders title, author, narrator, and the current chapter heading", () => {
    render(
      <NowListening
        contentId="book-1"
        title="Project Hail Mary"
        author="Andy Weir"
        narrator="Ray Porter"
        posterUrl="/p.jpg"
        playback={makePlayback({
          currentChapter: {
            index: 6,
            title: "The Astrophage",
            start_seconds: 0,
            end_seconds: 100,
            source: "embedded",
          },
        })}
        onCollapse={vi.fn()}
      />,
    );
    expect(screen.getByRole("heading", { name: "Project Hail Mary" })).toBeInTheDocument();
    expect(screen.getByText("Andy Weir")).toBeInTheDocument();
    expect(screen.getByText(/Ray Porter/)).toBeInTheDocument();
    expect(screen.getByText("The Astrophage")).toBeInTheDocument();
  });

  it("calls onCollapse when the collapse button is clicked", async () => {
    const onCollapse = vi.fn();
    render(
      <NowListening
        contentId="book-1"
        title="X"
        posterUrl=""
        playback={makePlayback()}
        onCollapse={onCollapse}
      />,
    );
    await userEvent.click(screen.getByRole("button", { name: /collapse/i }));
    expect(onCollapse).toHaveBeenCalled();
  });

  it("toggles between remaining and total time when the right time is clicked", async () => {
    render(
      <NowListening
        contentId="book-1"
        title="X"
        posterUrl=""
        playback={makePlayback({ currentTime: 100, duration: 600 })}
        onCollapse={vi.fn()}
      />,
    );
    expect(screen.getByTestId("now-listening-right-time")).toHaveTextContent("10:00");
    await userEvent.click(screen.getByTestId("now-listening-right-time"));
    expect(screen.getByTestId("now-listening-right-time")).toHaveTextContent("-8:20");
  });
});
```

- [ ] **Step 2: Run test — verify it fails**

Run: `cd web && pnpm test -- --run src/pages/audiobooks/player/NowListening.test.tsx`
Expected: FAIL

- [ ] **Step 3: Implement the overlay**

Create `web/src/pages/audiobooks/player/NowListening.tsx`:

```tsx
import { useState } from "react";
import { ChevronDown, MoreHorizontal, Pause, Play, RotateCcw, RotateCw } from "lucide-react";
import { SeekBar, formatTime } from "@/player/components/SeekBar";
import { ChaptersMenu } from "@/player/components/ChaptersMenu";
import { CircleButton } from "@/player/components/CircleButton";
import { SpeedMenu } from "@/player/components/SpeedMenu";
import type { AudiobookPlayback } from "./useAudiobookPlayback";

const SKIP_BACK_SECONDS = 30;
const SKIP_FORWARD_SECONDS = 30;
const PLAYBACK_RATES = [0.75, 1, 1.25, 1.5, 2] as const;

interface NowListeningProps {
  contentId: string;
  title: string;
  author?: string;
  narrator?: string;
  posterUrl: string;
  playback: AudiobookPlayback;
  onCollapse: () => void;
}

export function NowListening({
  contentId,
  title,
  author,
  narrator,
  posterUrl,
  playback,
  onCollapse,
}: NowListeningProps) {
  const [showRemaining, setShowRemaining] = useState(false);

  const rightTimeLabel = showRemaining
    ? `-${formatTime(Math.max(0, playback.duration - playback.currentTime))}`
    : formatTime(playback.duration);

  return (
    <div className="bg-background fixed inset-0 z-50 flex flex-col">
      <div className="flex items-center justify-between px-6 py-4">
        <button
          type="button"
          onClick={onCollapse}
          aria-label="Collapse player"
          className="text-muted-foreground hover:text-foreground rounded p-1.5"
        >
          <ChevronDown className="h-5 w-5" />
        </button>
        <button
          type="button"
          aria-label="More"
          className="text-muted-foreground hover:text-foreground rounded p-1.5"
        >
          <MoreHorizontal className="h-5 w-5" />
        </button>
      </div>

      <div className="grid flex-1 grid-cols-1 items-center gap-10 px-6 pb-6 md:grid-cols-[auto_1fr] md:px-16">
        <div className="mx-auto w-full max-w-[360px] md:mx-0">
          <div
            className="bg-muted aspect-[2/3] w-full overflow-hidden rounded-2xl shadow-2xl"
            style={{ viewTransitionName: `audiobook-cover-${contentId}` }}
          >
            {posterUrl ? (
              <img src={posterUrl} alt={title} className="h-full w-full object-cover" />
            ) : null}
          </div>
        </div>

        <div className="flex max-w-xl flex-col gap-6 md:gap-8">
          <div className="space-y-1">
            <h1 className="text-3xl font-semibold tracking-tight">{title}</h1>
            {author && <p className="text-muted-foreground text-base">{author}</p>}
            {narrator && (
              <p className="text-muted-foreground text-sm">
                Narrated by <span className="text-foreground">{narrator}</span>
              </p>
            )}
          </div>

          {playback.currentChapter && (
            <div className="space-y-1">
              <p className="text-muted-foreground text-[11px] tracking-[0.18em] uppercase">
                Chapter {playback.currentChapter.index + 1}
              </p>
              <p className="text-foreground text-lg font-medium">
                {playback.currentChapter.title}
              </p>
            </div>
          )}

          <div className="space-y-2">
            <SeekBar
              currentTime={playback.currentTime}
              duration={playback.duration}
              buffered={playback.buffered}
              chapters={playback.chapters}
              introRegion={null}
              creditsRegion={null}
              onSeek={playback.seekTo}
            />
            <div className="text-muted-foreground flex items-center justify-between text-xs tabular-nums">
              <span>{formatTime(playback.currentTime)}</span>
              <button
                type="button"
                data-testid="now-listening-right-time"
                onClick={() => setShowRemaining((v) => !v)}
                className="hover:text-foreground transition-colors"
              >
                {rightTimeLabel}
              </button>
            </div>
          </div>

          <div className="flex items-center justify-center gap-4">
            <CircleButton
              size="sm"
              variant="secondary"
              ariaLabel={`Back ${SKIP_BACK_SECONDS} seconds`}
              onClick={() => playback.skip(-SKIP_BACK_SECONDS)}
              disabled={!playback.hasFile}
            >
              <SkipIcon direction="back" seconds={SKIP_BACK_SECONDS} />
            </CircleButton>

            <CircleButton
              size="lg"
              variant="primary"
              ariaLabel={playback.playing ? "Pause" : "Play"}
              onClick={playback.togglePlay}
              disabled={!playback.hasFile}
              data-paused={!playback.playing}
            >
              {playback.playing ? (
                <Pause className="h-8 w-8" strokeWidth={0} fill="currentColor" />
              ) : (
                <Play className="ml-[2px] h-8 w-8" strokeWidth={0} fill="currentColor" />
              )}
            </CircleButton>

            <CircleButton
              size="sm"
              variant="secondary"
              ariaLabel={`Forward ${SKIP_FORWARD_SECONDS} seconds`}
              onClick={() => playback.skip(SKIP_FORWARD_SECONDS)}
              disabled={!playback.hasFile}
            >
              <SkipIcon direction="forward" seconds={SKIP_FORWARD_SECONDS} />
            </CircleButton>
          </div>

          <div className="text-muted-foreground flex items-center justify-center gap-6 text-sm">
            {/* Sleep timer slot arrives in Task 10 */}
            {playback.chapters.length > 0 && (
              <ChaptersMenu
                chapters={playback.chapters}
                currentTime={playback.currentTime}
                onSeek={playback.seekTo}
              />
            )}
            <SpeedMenu rates={PLAYBACK_RATES} value={playback.rate} onChange={playback.setRate} />
          </div>
        </div>
      </div>
    </div>
  );
}

function SkipIcon({ direction, seconds }: { direction: "back" | "forward"; seconds: number }) {
  const Arrow = direction === "back" ? RotateCcw : RotateCw;
  return (
    <span className="relative flex h-7 w-7 items-center justify-center">
      <Arrow className="h-7 w-7" strokeWidth={1.6} />
      <span className="absolute inset-0 flex items-center justify-center pb-[1px] text-[8.5px] font-semibold tracking-tight tabular-nums">
        {seconds}
      </span>
    </span>
  );
}
```

- [ ] **Step 4: Run tests + lint**

Run: `cd web && pnpm test -- --run src/pages/audiobooks/player/NowListening.test.tsx`
Expected: PASS

Run: `cd web && pnpm run lint && pnpm run format:check`
Expected: clean

- [ ] **Step 5: Commit**

```bash
git add web/src/pages/audiobooks/player/NowListening.tsx \
        web/src/pages/audiobooks/player/NowListening.test.tsx
git commit -m "feat(audiobooks): build NowListening overlay component"
```

---

## Task 9: Wire mode switching in `AudiobookPlayer`

**Files:**
- Modify: `web/src/pages/audiobooks/player/AudiobookPlayer.tsx`
- Modify: `web/src/pages/audiobooks/AudiobookDetail.tsx` (pass `author` + `narrator` to player)

Track a `"mini" | "now-listening"` mode locally. `MiniBar`'s `onExpand` flips to `"now-listening"`; `NowListening`'s `onCollapse` flips back. The audio element lives in the shared parent so the mode swap never touches it.

- [ ] **Step 1: Update `AudiobookPlayer.tsx`**

Replace `web/src/pages/audiobooks/player/AudiobookPlayer.tsx` with:

```tsx
import { useState } from "react";
import type { AudiobookFile } from "@/lib/audiobooks/types";
import { useAudiobookPlayback } from "./useAudiobookPlayback";
import { MiniBar } from "./MiniBar";
import { NowListening } from "./NowListening";

export interface AudiobookPlayerProps {
  contentId: string;
  title: string;
  author?: string;
  narrator?: string;
  posterUrl?: string;
  files: AudiobookFile[];
  initialPositionSeconds?: number;
  autoPlay?: boolean;
  onClose?: () => void;
}

export default function AudiobookPlayer({
  contentId,
  title,
  author,
  narrator,
  posterUrl,
  files,
  initialPositionSeconds = 0,
  autoPlay = true,
  onClose,
}: AudiobookPlayerProps) {
  const playback = useAudiobookPlayback({
    contentId,
    files,
    initialPositionSeconds,
    autoPlay,
  });
  const [mode, setMode] = useState<"mini" | "now-listening">("mini");

  return (
    <>
      {playback.hasFile && (
        <audio
          ref={playback.audioRef}
          src={playback.streamUrl}
          preload="metadata"
          style={{ display: "none" }}
        />
      )}
      {mode === "mini" ? (
        <MiniBar
          contentId={contentId}
          title={title}
          posterUrl={posterUrl}
          playback={playback}
          onClose={onClose}
          onExpand={() => setMode("now-listening")}
        />
      ) : (
        <NowListening
          contentId={contentId}
          title={title}
          author={author}
          narrator={narrator}
          posterUrl={posterUrl ?? ""}
          playback={playback}
          onCollapse={() => setMode("mini")}
        />
      )}
    </>
  );
}
```

- [ ] **Step 2: Pass author + narrator from `AudiobookDetail.tsx`**

In `web/src/pages/audiobooks/AudiobookDetail.tsx`, update the `<AudiobookPlayer>` element to pass:

```tsx
author={author}
narrator={narrator}
```

(These are already destructured from `data` higher in the file.)

Also drop the `<div className="bg-background fixed inset-x-0 bottom-0 z-40 border-t shadow-lg">` wrapper that lives around `<AudiobookPlayer>` today — the player itself now owns its positioning. Replace the wrapper block with:

```tsx
{playerOpen && (
  <AudiobookPlayer
    key={`${contentId}-${startSeconds}`}
    contentId={contentId ?? ""}
    title={audiobook.title}
    author={author}
    narrator={narrator}
    posterUrl={audiobook.poster_url}
    files={files}
    initialPositionSeconds={startSeconds}
    onClose={() => setPlayerOpen(false)}
  />
)}
```

Then move the fixed-bottom wrapper *into* `MiniBar.tsx`:

In `web/src/pages/audiobooks/player/MiniBar.tsx`, wrap the existing returned JSX in:

```tsx
<div className="bg-background fixed inset-x-0 bottom-0 z-40 border-t shadow-lg">
  {/* existing inner div with bg-background border-b ... */}
</div>
```

(Keep the existing inner styling — the wrapper just handles position. Drop `bg-background border-b` from the inner div since it's now redundant with the wrapper.)

- [ ] **Step 3: Manual verification**

Run: `cd web && pnpm dev`

- Open an audiobook detail page, click Play. Mini bar appears at the bottom.
- Click the cover tile. The Now Listening overlay appears full-screen.
- Click the chevron-down (top-left). You return to the mini bar; audio keeps playing without restart.
- Confirm `audio.currentTime` advances continuously through the swap (use DevTools to check the `<audio>` element's `currentTime` before and after — should not reset).

- [ ] **Step 4: Run tests + lint**

Run: `cd web && pnpm test -- --run src/pages/audiobooks/player`
Expected: PASS (all player tests still green)

Run: `cd web && pnpm run lint && pnpm run format:check`
Expected: clean

- [ ] **Step 5: Commit**

```bash
git add web/src/pages/audiobooks/player/AudiobookPlayer.tsx \
        web/src/pages/audiobooks/player/MiniBar.tsx \
        web/src/pages/audiobooks/AudiobookDetail.tsx
git commit -m "feat(audiobooks): wire mini ↔ Now Listening mode swap"
```

---

## Task 10: Sleep timer behavior + `SleepTimerMenu` primitive

**Files:**
- Create: `web/src/player/components/SleepTimerMenu.tsx`
- Create: `web/src/player/components/SleepTimerMenu.test.tsx`
- Modify: `web/src/pages/audiobooks/player/useAudiobookPlayback.ts` (add sleep state + fade)
- Modify: `web/src/pages/audiobooks/player/useAudiobookPlayback.test.ts`
- Modify: `web/src/pages/audiobooks/player/MiniBar.tsx` (insert menu into right rail)
- Modify: `web/src/pages/audiobooks/player/NowListening.tsx` (insert into utility row)

End-of-chapter is computed against `playback.currentChapter.end_seconds`. Other presets are absolute durations in seconds.

- [ ] **Step 1: Write the failing tests for the menu**

Create `web/src/player/components/SleepTimerMenu.test.tsx`:

```tsx
import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { SleepTimerMenu, type SleepSetting } from "./SleepTimerMenu";

describe("SleepTimerMenu", () => {
  it("shows 'Sleep' when off and the countdown when armed", () => {
    const { rerender } = render(
      <SleepTimerMenu setting={{ kind: "off" }} remainingMs={null} onChange={() => {}} />,
    );
    expect(screen.getByRole("button", { name: /sleep timer/i })).toHaveTextContent("Sleep");
    rerender(
      <SleepTimerMenu
        setting={{ kind: "duration", seconds: 300 }}
        remainingMs={272_000}
        onChange={() => {}}
      />,
    );
    expect(screen.getByRole("button", { name: /sleep timer/i })).toHaveTextContent("Sleep 4:32");
  });

  it("emits a duration setting when a preset is chosen", async () => {
    const onChange = vi.fn();
    render(<SleepTimerMenu setting={{ kind: "off" }} remainingMs={null} onChange={onChange} />);
    await userEvent.click(screen.getByRole("button", { name: /sleep timer/i }));
    await userEvent.click(screen.getByRole("menuitem", { name: "15 min" }));
    expect(onChange).toHaveBeenCalledWith({ kind: "duration", seconds: 900 } as SleepSetting);
  });

  it("emits end-of-chapter when that option is chosen", async () => {
    const onChange = vi.fn();
    render(<SleepTimerMenu setting={{ kind: "off" }} remainingMs={null} onChange={onChange} />);
    await userEvent.click(screen.getByRole("button", { name: /sleep timer/i }));
    await userEvent.click(screen.getByRole("menuitem", { name: /end of chapter/i }));
    expect(onChange).toHaveBeenCalledWith({ kind: "end-of-chapter" });
  });
});
```

- [ ] **Step 2: Run test — verify it fails**

Run: `cd web && pnpm test -- --run src/player/components/SleepTimerMenu.test.tsx`
Expected: FAIL — module not found

- [ ] **Step 3: Implement the menu**

Create `web/src/player/components/SleepTimerMenu.tsx`:

```tsx
import { useCallback, useEffect, useRef, useState } from "react";
import { Moon } from "lucide-react";

export type SleepSetting =
  | { kind: "off" }
  | { kind: "duration"; seconds: number }
  | { kind: "end-of-chapter" };

interface SleepTimerMenuProps {
  setting: SleepSetting;
  remainingMs: number | null;
  onChange: (next: SleepSetting) => void;
}

const PRESETS: { label: string; seconds: number }[] = [
  { label: "5 min", seconds: 300 },
  { label: "15 min", seconds: 900 },
  { label: "30 min", seconds: 1800 },
  { label: "45 min", seconds: 2700 },
  { label: "60 min", seconds: 3600 },
];

function formatCountdown(ms: number): string {
  const total = Math.max(0, Math.ceil(ms / 1000));
  const m = Math.floor(total / 60);
  const s = total % 60;
  return `${m}:${String(s).padStart(2, "0")}`;
}

export function SleepTimerMenu({ setting, remainingMs, onChange }: SleepTimerMenuProps) {
  const [open, setOpen] = useState(false);
  const menuRef = useRef<HTMLDivElement>(null);
  const itemsRef = useRef<(HTMLButtonElement | null)[]>([]);
  const armed = setting.kind !== "off";

  const handleBlur = useCallback((e: React.FocusEvent) => {
    if (!menuRef.current?.contains(e.relatedTarget as Node)) setOpen(false);
  }, []);

  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && setOpen(false);
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [open]);

  const label = armed && remainingMs != null ? `Sleep ${formatCountdown(remainingMs)}` : "Sleep";

  return (
    <div ref={menuRef} className="relative" onBlur={handleBlur}>
      <button
        type="button"
        className="player-utility-btn flex items-center gap-1.5 px-2 text-xs"
        onClick={() => setOpen((v) => !v)}
        aria-label="Sleep timer"
        aria-expanded={open}
        aria-haspopup="menu"
      >
        <Moon className="h-3.5 w-3.5" />
        <span className="tabular-nums">{label}</span>
      </button>

      {open && (
        <div
          role="menu"
          className="absolute right-0 bottom-full mb-2 flex min-w-[160px] flex-col overflow-hidden rounded-lg bg-black/90 py-1.5 shadow-xl backdrop-blur-sm"
        >
          {armed && (
            <button
              ref={(el) => {
                itemsRef.current[0] = el;
              }}
              role="menuitem"
              type="button"
              className="w-full px-4 py-2 text-left text-sm text-white/85 hover:bg-white/10"
              onClick={() => {
                onChange({ kind: "off" });
                setOpen(false);
              }}
            >
              Turn off
            </button>
          )}
          {PRESETS.map((p, i) => (
            <button
              key={p.seconds}
              ref={(el) => {
                itemsRef.current[(armed ? 1 : 0) + i] = el;
              }}
              role="menuitem"
              type="button"
              className="w-full px-4 py-2 text-left text-sm text-white/85 hover:bg-white/10"
              onClick={() => {
                onChange({ kind: "duration", seconds: p.seconds });
                setOpen(false);
              }}
            >
              {p.label}
            </button>
          ))}
          <button
            role="menuitem"
            type="button"
            className="w-full px-4 py-2 text-left text-sm text-white/85 hover:bg-white/10"
            onClick={() => {
              onChange({ kind: "end-of-chapter" });
              setOpen(false);
            }}
          >
            End of chapter
          </button>
        </div>
      )}
    </div>
  );
}
```

- [ ] **Step 4: Run menu test — verify PASS**

Run: `cd web && pnpm test -- --run src/player/components/SleepTimerMenu.test.tsx`
Expected: PASS

- [ ] **Step 5: Extend `useAudiobookPlayback` test**

Add to `web/src/pages/audiobooks/player/useAudiobookPlayback.test.ts`:

```ts
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
```

- [ ] **Step 6: Extend `useAudiobookPlayback` implementation**

Add to `web/src/pages/audiobooks/player/useAudiobookPlayback.ts`:

1. Import `SleepSetting`:

```ts
import type { SleepSetting } from "@/player/components/SleepTimerMenu";
```

2. Extend the `AudiobookPlayback` interface:

```ts
sleep: { setting: SleepSetting; remainingMs: number | null };
setSleep: (next: SleepSetting) => void;
```

3. Inside the hook, add (before the `return`):

```ts
const [sleepSetting, setSleepSetting] = useState<SleepSetting>({ kind: "off" });
const [sleepTargetMs, setSleepTargetMs] = useState<number | null>(null);
const [sleepNowMs, setSleepNowMs] = useState<number>(() => Date.now());

useEffect(() => {
  if (sleepSetting.kind !== "duration") {
    setSleepTargetMs(null);
    return;
  }
  setSleepTargetMs(Date.now() + sleepSetting.seconds * 1000);
}, [sleepSetting]);

useEffect(() => {
  if (sleepTargetMs == null) return;
  const id = window.setInterval(() => setSleepNowMs(Date.now()), 1000);
  return () => window.clearInterval(id);
}, [sleepTargetMs]);

useEffect(() => {
  if (sleepTargetMs == null) return;
  if (sleepNowMs < sleepTargetMs) return;
  const audio = audioRef.current;
  if (audio && !audio.paused) audio.pause();
  setSleepSetting({ kind: "off" });
  setSleepTargetMs(null);
}, [sleepNowMs, sleepTargetMs]);

useEffect(() => {
  if (sleepSetting.kind !== "end-of-chapter" || !currentChapter) return;
  if (currentTime < currentChapter.end_seconds) return;
  const audio = audioRef.current;
  if (audio && !audio.paused) audio.pause();
  setSleepSetting({ kind: "off" });
}, [sleepSetting, currentChapter, currentTime]);

const setSleep = useCallback((next: SleepSetting) => setSleepSetting(next), []);
const sleepRemainingMs = sleepTargetMs == null ? null : Math.max(0, sleepTargetMs - sleepNowMs);
```

4. Add to the returned object:

```ts
sleep: { setting: sleepSetting, remainingMs: sleepRemainingMs },
setSleep,
```

- [ ] **Step 7: Wire `SleepTimerMenu` into MiniBar and NowListening**

In `web/src/pages/audiobooks/player/MiniBar.tsx`, add to the right rail (before `ChaptersMenu`):

```tsx
import { SleepTimerMenu } from "@/player/components/SleepTimerMenu";

// ... inside the right rail div, first child:
<SleepTimerMenu
  setting={playback.sleep.setting}
  remainingMs={playback.sleep.remainingMs}
  onChange={playback.setSleep}
/>
```

In `web/src/pages/audiobooks/player/NowListening.tsx`, add to the utility row (first item, before `ChaptersMenu`):

```tsx
import { SleepTimerMenu } from "@/player/components/SleepTimerMenu";

// ... first child of the utility row:
<SleepTimerMenu
  setting={playback.sleep.setting}
  remainingMs={playback.sleep.remainingMs}
  onChange={playback.setSleep}
/>
```

- [ ] **Step 8: Update test helpers**

In `MiniBar.test.tsx` and `NowListening.test.tsx`, update `makePlayback` to include:

```ts
sleep: { setting: { kind: "off" as const }, remainingMs: null },
setSleep: vi.fn(),
```

- [ ] **Step 9: Run all related tests + lint**

Run: `cd web && pnpm test -- --run src/player/components/SleepTimerMenu.test.tsx src/pages/audiobooks/player`
Expected: PASS

Run: `cd web && pnpm run lint && pnpm run format:check`
Expected: clean

- [ ] **Step 10: Commit**

```bash
git add web/src/player/components/SleepTimerMenu.tsx \
        web/src/player/components/SleepTimerMenu.test.tsx \
        web/src/pages/audiobooks/player/useAudiobookPlayback.ts \
        web/src/pages/audiobooks/player/useAudiobookPlayback.test.ts \
        web/src/pages/audiobooks/player/MiniBar.tsx \
        web/src/pages/audiobooks/player/MiniBar.test.tsx \
        web/src/pages/audiobooks/player/NowListening.tsx \
        web/src/pages/audiobooks/player/NowListening.test.tsx
git commit -m "feat(audiobooks): add sleep timer with duration + end-of-chapter modes"
```

---

## Task 11: Detail page hero — progress + chapter-aware Resume

**Files:**
- Modify: `web/src/pages/audiobooks/AudiobookDetail.tsx`

Move the progress bar above the actions row, show "Xh Ym listened · NN%", and put the destination chapter name on the Resume button.

- [ ] **Step 1: Compute the resume chapter**

In `web/src/pages/audiobooks/AudiobookDetail.tsx`, add a helper near the top (after `formatSeconds`):

```tsx
function findChapterAt(
  chapters: ReturnType<typeof buildChapterList>,
  seconds: number,
): { label: string; index: number } | null {
  for (let i = chapters.length - 1; i >= 0; i--) {
    if (seconds >= chapters[i].absoluteStart) {
      return { label: chapters[i].label, index: i + 1 };
    }
  }
  return chapters[0] ? { label: chapters[0].label, index: 1 } : null;
}
```

- [ ] **Step 2: Replace the `actions` slot of `<DetailHero>`**

In the `<DetailHero ... actions={...}>` block, replace the actions slot with:

```tsx
actions={
  files.length > 0 && (
    <div className="flex max-w-md flex-col gap-3">
      {hasProgress && durationTotal > 0 && (
        <div>
          <div className="bg-muted h-1.5 w-full overflow-hidden rounded-full">
            <div
              className="bg-primary h-full rounded-full transition-all"
              style={{ width: `${Math.min(100, (resumeSeconds / durationTotal) * 100)}%` }}
            />
          </div>
          <p className="text-muted-foreground mt-1 text-xs">
            {formatSeconds(resumeSeconds)} listened ·{" "}
            {Math.round((resumeSeconds / durationTotal) * 100)}%
          </p>
        </div>
      )}
      <div className="flex flex-wrap items-center gap-3">
        <Button onClick={handlePlayResume} size="lg" className="gap-2">
          <Play className="h-4 w-4 fill-current" />
          {hasProgress ? (
            (() => {
              const ch = findChapterAt(buildChapterList(files), resumeSeconds);
              return ch ? `Resume · ${ch.label}` : "Resume";
            })()
          ) : (
            "Play"
          )}
        </Button>
        {hasProgress && (
          <Button variant="outline" size="lg" onClick={() => openPlayer(0)}>
            Play from Start
          </Button>
        )}
      </div>
    </div>
  )
}
```

- [ ] **Step 3: Manual verification**

Run: `cd web && pnpm dev`. Visit an audiobook with progress already saved.
- Progress bar appears above the Resume button.
- Caption shows e.g. "3h 12m listened · 19%".
- Resume button label shows the destination chapter (e.g. "Resume · The Astrophage").
- Visit an audiobook with no progress: Play button reads "Play", no progress bar, no Resume label.

- [ ] **Step 4: Lint + commit**

```bash
cd web && pnpm run lint && pnpm run format:check
```

```bash
git add web/src/pages/audiobooks/AudiobookDetail.tsx
git commit -m "feat(audiobooks): hero shows progress + chapter-aware Resume label"
```

---

## Task 12: Detail page chapters — `ChaptersSection` component

**Files:**
- Create: `web/src/pages/audiobooks/components/ChaptersSection.tsx`
- Create: `web/src/pages/audiobooks/components/ChaptersSection.test.tsx`
- Modify: `web/src/pages/audiobooks/AudiobookDetail.tsx` (replace inline `ChapterList` with `ChaptersSection`)

Replace today's collapsed-by-default disclosure with an always-expanded section, highlight the currently-playing chapter, and add a lightweight sort menu (Position / Longest first).

- [ ] **Step 1: Write the failing test**

Create `web/src/pages/audiobooks/components/ChaptersSection.test.tsx`:

```tsx
import { describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ChaptersSection } from "./ChaptersSection";
import type { AudiobookFile } from "@/lib/audiobooks/types";

const files: AudiobookFile[] = [
  {
    id: 1,
    path: "a",
    duration_seconds: 600,
    chapters: [
      { index: 0, title: "Prologue", source: "embedded", start_seconds: 0, end_seconds: 200 },
      { index: 1, title: "Memory", source: "embedded", start_seconds: 200, end_seconds: 600 },
    ],
  },
];

describe("ChaptersSection", () => {
  it("renders chapters expanded by default", () => {
    render(
      <ChaptersSection files={files} currentPositionSeconds={null} onSelect={vi.fn()} />,
    );
    expect(screen.getByRole("button", { name: /Prologue/ })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Memory/ })).toBeInTheDocument();
  });

  it("highlights the currently-playing chapter", () => {
    render(
      <ChaptersSection files={files} currentPositionSeconds={250} onSelect={vi.fn()} />,
    );
    const row = screen.getByRole("button", { name: /Memory/ });
    expect(row).toHaveAttribute("data-current", "true");
  });

  it("sort menu switches between position and longest-first orders", async () => {
    render(
      <ChaptersSection files={files} currentPositionSeconds={null} onSelect={vi.fn()} />,
    );
    const rowsBefore = screen.getAllByRole("button", { name: /Prologue|Memory/ });
    expect(within(rowsBefore[0]).getByText("Prologue")).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: /sort/i }));
    await userEvent.click(screen.getByRole("menuitem", { name: /longest first/i }));

    const rowsAfter = screen.getAllByRole("button", { name: /Prologue|Memory/ });
    expect(within(rowsAfter[0]).getByText("Memory")).toBeInTheDocument();
  });

  it("calls onSelect with absolute start seconds when a chapter is clicked", async () => {
    const onSelect = vi.fn();
    render(<ChaptersSection files={files} currentPositionSeconds={null} onSelect={onSelect} />);
    await userEvent.click(screen.getByRole("button", { name: /Memory/ }));
    expect(onSelect).toHaveBeenCalledWith(200);
  });
});
```

- [ ] **Step 2: Run test — verify it fails**

Run: `cd web && pnpm test -- --run src/pages/audiobooks/components/ChaptersSection.test.tsx`
Expected: FAIL

- [ ] **Step 3: Implement `ChaptersSection`**

Create `web/src/pages/audiobooks/components/ChaptersSection.tsx`:

```tsx
import { useMemo, useState } from "react";
import { ArrowUpDown, Play } from "lucide-react";
import type { AudiobookChapter, AudiobookFile } from "@/lib/audiobooks/types";

interface ChaptersSectionProps {
  files: AudiobookFile[];
  currentPositionSeconds: number | null;
  onSelect: (absoluteSeconds: number) => void;
}

type SortMode = "position" | "longest-first";

interface Row {
  chapter: AudiobookChapter;
  absoluteStart: number;
  durationSeconds: number;
  label: string;
  positionIndex: number; // 1-based
}

function buildRows(files: AudiobookFile[]): Row[] {
  const rows: Row[] = [];
  let offset = 0;
  let positionIndex = 1;
  for (const file of files) {
    if (file.chapters) {
      for (const ch of file.chapters) {
        rows.push({
          chapter: ch,
          absoluteStart: offset + ch.start_seconds,
          durationSeconds: Math.max(0, (ch.end_seconds ?? ch.start_seconds) - ch.start_seconds),
          label: ch.title || `Chapter ${ch.index + 1}`,
          positionIndex: positionIndex++,
        });
      }
    }
    offset += file.duration_seconds ?? 0;
  }
  return rows;
}

function formatChapterDuration(s: number): string {
  if (s <= 0) return "";
  const m = Math.floor(s / 60);
  const sec = Math.floor(s % 60);
  return `${m}m ${String(sec).padStart(2, "0")}s`;
}

function formatChapterStart(totalSeconds: number): string {
  const h = Math.floor(totalSeconds / 3600);
  const m = Math.floor((totalSeconds % 3600) / 60);
  const s = Math.floor(totalSeconds % 60);
  return `${h}:${String(m).padStart(2, "0")}:${String(s).padStart(2, "0")}`;
}

export function ChaptersSection({
  files,
  currentPositionSeconds,
  onSelect,
}: ChaptersSectionProps) {
  const rows = useMemo(() => buildRows(files), [files]);
  const [sort, setSort] = useState<SortMode>("position");
  const [sortOpen, setSortOpen] = useState(false);

  const sorted = useMemo(() => {
    if (sort === "longest-first") {
      return [...rows].sort((a, b) => b.durationSeconds - a.durationSeconds);
    }
    return rows;
  }, [rows, sort]);

  const currentIndex = useMemo(() => {
    if (currentPositionSeconds == null) return -1;
    for (let i = rows.length - 1; i >= 0; i--) {
      if (currentPositionSeconds >= rows[i].absoluteStart) return i;
    }
    return -1;
  }, [rows, currentPositionSeconds]);

  if (rows.length === 0) return null;

  return (
    <section className="mt-10">
      <div className="mb-4 flex items-center justify-between">
        <h2 className="text-xl font-semibold tracking-tight">
          Chapters
          <span className="text-muted-foreground ml-2 text-sm font-normal">({rows.length})</span>
        </h2>
        <div className="relative">
          <button
            type="button"
            aria-label="Sort chapters"
            aria-haspopup="menu"
            aria-expanded={sortOpen}
            className="text-muted-foreground hover:text-foreground flex items-center gap-1 text-xs"
            onClick={() => setSortOpen((v) => !v)}
          >
            <ArrowUpDown className="h-3.5 w-3.5" />
            Sort
          </button>
          {sortOpen && (
            <div
              role="menu"
              className="bg-popover absolute right-0 mt-2 w-44 overflow-hidden rounded-md border shadow-lg"
            >
              {(["position", "longest-first"] as SortMode[]).map((mode) => (
                <button
                  key={mode}
                  role="menuitem"
                  type="button"
                  data-active={mode === sort ? "true" : undefined}
                  className="hover:bg-muted/60 w-full px-3 py-2 text-left text-sm"
                  onClick={() => {
                    setSort(mode);
                    setSortOpen(false);
                  }}
                >
                  {mode === "position" ? "By position" : "Longest first"}
                </button>
              ))}
            </div>
          )}
        </div>
      </div>

      <ol className="divide-border divide-y rounded-xl border">
        {sorted.map((row) => {
          const isCurrent = rows[currentIndex]?.chapter === row.chapter;
          return (
            <li key={`${row.chapter.index}-${row.absoluteStart}`}>
              <button
                type="button"
                onClick={() => onSelect(row.absoluteStart)}
                data-current={isCurrent ? "true" : undefined}
                className={`hover:bg-muted/50 flex w-full items-center gap-3 px-4 py-3 text-left transition-colors ${
                  isCurrent ? "bg-muted/50 border-primary border-l-2" : ""
                }`}
              >
                <span className="text-muted-foreground w-8 shrink-0 text-right text-xs tabular-nums">
                  {row.positionIndex}
                </span>
                <span className="min-w-0 flex-1 truncate text-sm font-medium">{row.label}</span>
                <span className="text-muted-foreground hidden shrink-0 font-mono text-xs sm:inline">
                  {formatChapterStart(row.absoluteStart)}
                </span>
                <span className="text-muted-foreground shrink-0 font-mono text-xs">
                  {formatChapterDuration(row.durationSeconds)}
                </span>
                {isCurrent && (
                  <span className="text-primary flex shrink-0 items-center gap-1 text-xs">
                    <Play className="h-3 w-3 fill-current" /> playing
                  </span>
                )}
              </button>
            </li>
          );
        })}
      </ol>
    </section>
  );
}
```

- [ ] **Step 4: Replace inline `ChapterList` in `AudiobookDetail.tsx`**

In `web/src/pages/audiobooks/AudiobookDetail.tsx`:
1. Delete the inline `ChapterList` component (lines ~68-121) and the related `buildChapterList` if it's no longer used elsewhere (it IS still used by Task 11's `findChapterAt` helper — keep it).
2. Replace the `<ChapterList files={files} onSelect={...} />` usage with:

```tsx
<ChaptersSection
  files={files}
  currentPositionSeconds={playerOpen ? startSeconds : resumeSeconds || null}
  onSelect={(s) => openPlayer(s)}
/>
```

3. Add import:

```tsx
import { ChaptersSection } from "./components/ChaptersSection";
```

Note: `currentPositionSeconds` here uses the start position the player was opened with as a proxy. A future improvement is lifting `currentTime` from the player so the highlight tracks in real time — out of scope for v1.

- [ ] **Step 5: Run tests + lint**

Run: `cd web && pnpm test -- --run src/pages/audiobooks/components/ChaptersSection.test.tsx`
Expected: PASS

Run: `cd web && pnpm run lint && pnpm run format:check`
Expected: clean

- [ ] **Step 6: Commit**

```bash
git add web/src/pages/audiobooks/components/ChaptersSection.tsx \
        web/src/pages/audiobooks/components/ChaptersSection.test.tsx \
        web/src/pages/audiobooks/AudiobookDetail.tsx
git commit -m "feat(audiobooks): chapters expanded by default with current-row highlight"
```

---

## Task 13: Detail page — `NarratorCard` component

**Files:**
- Create: `web/src/pages/audiobooks/components/NarratorCard.tsx`
- Modify: `web/src/pages/audiobooks/AudiobookDetail.tsx`

Show the narrator as a first-class card. Render only when `data.narrator` is non-empty. No "n audiobooks" count for v1 — that needs backend support and the spec defers it.

- [ ] **Step 1: Create the component**

Create `web/src/pages/audiobooks/components/NarratorCard.tsx`:

```tsx
interface NarratorCardProps {
  narrator: string;
}

function initials(name: string): string {
  return name
    .split(/\s+/)
    .filter(Boolean)
    .slice(0, 2)
    .map((w) => w[0]?.toUpperCase() ?? "")
    .join("");
}

export function NarratorCard({ narrator }: NarratorCardProps) {
  return (
    <section className="mt-10">
      <h2 className="mb-4 text-xl font-semibold tracking-tight">Narrator</h2>
      <div className="bg-muted/30 flex items-center gap-4 rounded-xl border p-4">
        <div className="bg-muted text-muted-foreground flex h-16 w-16 shrink-0 items-center justify-center rounded-full text-lg font-medium">
          {initials(narrator) || "?"}
        </div>
        <div className="min-w-0">
          <p className="truncate text-base font-medium">{narrator}</p>
        </div>
      </div>
    </section>
  );
}
```

- [ ] **Step 2: Mount in `AudiobookDetail.tsx`**

In `web/src/pages/audiobooks/AudiobookDetail.tsx`, after the `<ChaptersSection>` block:

```tsx
{narrator && <NarratorCard narrator={narrator} />}
```

Add import:

```tsx
import { NarratorCard } from "./components/NarratorCard";
```

- [ ] **Step 3: Manual verification**

Run `cd web && pnpm dev`. Open an audiobook with a narrator — the card appears. Open one without a narrator — no card, no empty space, no layout jump.

- [ ] **Step 4: Lint + commit**

```bash
cd web && pnpm run lint && pnpm run format:check
```

```bash
git add web/src/pages/audiobooks/components/NarratorCard.tsx \
        web/src/pages/audiobooks/AudiobookDetail.tsx
git commit -m "feat(audiobooks): add narrator card to detail page"
```

---

## Task 14: Detail page — feature-detected `RelatedRail`s

**Files:**
- Create: `web/src/pages/audiobooks/components/RelatedRail.tsx`
- Modify: `web/src/lib/audiobooks/types.ts` (add optional related arrays to the response type)
- Modify: `web/src/pages/audiobooks/AudiobookDetail.tsx`

Render "Also by author" and "In this series" only when the API response carries the arrays. The backend doesn't expose them yet — this task ships the frontend ready to display them as soon as the backend does, with zero impact today.

- [ ] **Step 1: Extend the response type**

In `web/src/lib/audiobooks/types.ts`, extend `AudiobookDetailResponse`:

```ts
export interface AudiobookRelatedItem {
  content_id: string;
  title: string;
  poster_url?: string;
  year?: number;
}

export interface AudiobookSeriesEntry {
  content_id: string;
  title: string;
  poster_url?: string;
  series_index?: number;
}

export interface AudiobookDetailResponse {
  audiobook: AudiobookDetailItem;
  author?: string;
  narrator?: string;
  files: AudiobookFile[];
  progress?: AudiobookProgress;
  also_by_author?: AudiobookRelatedItem[];
  in_series?: {
    name?: string;
    entries: AudiobookSeriesEntry[];
  };
}
```

- [ ] **Step 2: Create the rail**

Create `web/src/pages/audiobooks/components/RelatedRail.tsx`:

```tsx
import ViewTransitionLink from "@/components/ViewTransitionLink";

interface RelatedRailItem {
  content_id: string;
  title: string;
  poster_url?: string;
  subtitle?: string;
  highlight?: boolean;
}

interface RelatedRailProps {
  heading: string;
  items: RelatedRailItem[];
}

export function RelatedRail({ heading, items }: RelatedRailProps) {
  if (items.length === 0) return null;
  return (
    <section className="mt-10">
      <h2 className="mb-4 text-xl font-semibold tracking-tight">{heading}</h2>
      <div className="-mx-2 flex gap-3 overflow-x-auto px-2 pb-2">
        {items.map((item) => (
          <ViewTransitionLink
            key={item.content_id}
            to={`/audiobooks/book/${item.content_id}`}
            className={`block w-[112px] shrink-0 ${
              item.highlight ? "ring-primary rounded-lg ring-2 ring-offset-2" : ""
            }`}
          >
            <div className="bg-muted relative aspect-[2/3] overflow-hidden rounded-lg">
              {item.poster_url ? (
                <img
                  src={item.poster_url}
                  alt={item.title}
                  className="h-full w-full object-cover"
                  loading="lazy"
                />
              ) : null}
            </div>
            <div className="mt-2 truncate text-[13px] font-medium">{item.title}</div>
            {item.subtitle && (
              <div className="text-muted-foreground truncate text-[11px]">{item.subtitle}</div>
            )}
          </ViewTransitionLink>
        ))}
      </div>
    </section>
  );
}
```

- [ ] **Step 3: Mount the rails**

In `web/src/pages/audiobooks/AudiobookDetail.tsx`, after the `{narrator && <NarratorCard ... />}` block:

```tsx
{data.also_by_author && data.also_by_author.length > 0 && (
  <RelatedRail
    heading={`Also by ${author ?? "this author"}`}
    items={data.also_by_author.map((it) => ({
      content_id: it.content_id,
      title: it.title,
      poster_url: it.poster_url,
      subtitle: it.year ? String(it.year) : undefined,
    }))}
  />
)}

{data.in_series && data.in_series.entries.length > 0 && (
  <RelatedRail
    heading={data.in_series.name ? `In ${data.in_series.name}` : "In this series"}
    items={data.in_series.entries.map((it) => ({
      content_id: it.content_id,
      title: it.title,
      poster_url: it.poster_url,
      subtitle:
        typeof it.series_index === "number" ? `Book ${it.series_index}` : undefined,
      highlight: it.content_id === contentId,
    }))}
  />
)}
```

Add import:

```tsx
import { RelatedRail } from "./components/RelatedRail";
```

- [ ] **Step 4: Manual verification**

Run: `cd web && pnpm dev`. Open any audiobook detail page — the rails should NOT appear (backend doesn't ship the arrays yet). To smoke-test the render path locally, temporarily add a `also_by_author: [{ content_id: "x", title: "T" }]` to the API response in your dev backend OR mock the hook return; revert before commit.

- [ ] **Step 5: Lint + commit**

```bash
cd web && pnpm run lint && pnpm run format:check
```

```bash
git add web/src/lib/audiobooks/types.ts \
        web/src/pages/audiobooks/components/RelatedRail.tsx \
        web/src/pages/audiobooks/AudiobookDetail.tsx
git commit -m "feat(audiobooks): render Also-by-author / In-series rails when API exposes them"
```

---

## Task 14b: Embedding-based "Similar audiobooks" rail

**Files:**
- Modify: `web/src/lib/audiobooks/types.ts` (add optional `similar_audiobooks` array)
- Modify: `web/src/pages/audiobooks/components/RelatedRail.tsx` (accept optional `subtitle` prop)
- Modify: `web/src/pages/audiobooks/AudiobookDetail.tsx` (mount the third rail, placed FIRST)

Third feature-detected rail, same shape as the two from Task 14 but with a small subtitle to give the recommendation source a tiny bit of provenance copy. The backend computes similarity from per-book embeddings server-side and returns the top-N already ranked; the frontend has no embedding logic of its own.

- [ ] **Step 1: Extend the response type**

In `web/src/lib/audiobooks/types.ts`, extend `AudiobookDetailResponse` with one new optional field (the `AudiobookRelatedItem` type is already defined in Task 14):

```ts
export interface AudiobookDetailResponse {
  audiobook: AudiobookDetailItem;
  author?: string;
  narrator?: string;
  files: AudiobookFile[];
  progress?: AudiobookProgress;
  similar_audiobooks?: AudiobookRelatedItem[];
  also_by_author?: AudiobookRelatedItem[];
  in_series?: {
    name?: string;
    entries: AudiobookSeriesEntry[];
  };
}
```

- [ ] **Step 2: Add optional `subtitle` prop to `RelatedRail`**

In `web/src/pages/audiobooks/components/RelatedRail.tsx`, extend `RelatedRailProps` and render the subtitle under the heading when present:

```tsx
interface RelatedRailProps {
  heading: string;
  subtitle?: string;
  items: RelatedRailItem[];
}

export function RelatedRail({ heading, subtitle, items }: RelatedRailProps) {
  if (items.length === 0) return null;
  return (
    <section className="mt-10">
      <div className="mb-4">
        <h2 className="text-xl font-semibold tracking-tight">{heading}</h2>
        {subtitle && (
          <p className="text-muted-foreground mt-1 text-xs">{subtitle}</p>
        )}
      </div>
      <div className="-mx-2 flex gap-3 overflow-x-auto px-2 pb-2">
        {items.map((item) => (
          <ViewTransitionLink
            key={item.content_id}
            to={`/audiobooks/book/${item.content_id}`}
            className={`block w-[112px] shrink-0 ${
              item.highlight ? "ring-primary rounded-lg ring-2 ring-offset-2" : ""
            }`}
          >
            <div className="bg-muted relative aspect-[2/3] overflow-hidden rounded-lg">
              {item.poster_url ? (
                <img
                  src={item.poster_url}
                  alt={item.title}
                  className="h-full w-full object-cover"
                  loading="lazy"
                />
              ) : null}
            </div>
            <div className="mt-2 truncate text-[13px] font-medium">{item.title}</div>
            {item.subtitle && (
              <div className="text-muted-foreground truncate text-[11px]">{item.subtitle}</div>
            )}
          </ViewTransitionLink>
        ))}
      </div>
    </section>
  );
}
```

(The previous Task 14 mounted `RelatedRail` without `subtitle`, which remains valid — the prop is optional.)

- [ ] **Step 3: Mount the rail FIRST in `AudiobookDetail.tsx`**

Add the new rail block above the existing two from Task 14 (so the rendered order, top to bottom, is: Similar audiobooks → Also by author → In this series):

```tsx
{data.similar_audiobooks && data.similar_audiobooks.length > 0 && (
  <RelatedRail
    heading="Similar audiobooks"
    subtitle="Based on listening patterns"
    items={data.similar_audiobooks.map((it) => ({
      content_id: it.content_id,
      title: it.title,
      poster_url: it.poster_url,
      subtitle: it.year ? String(it.year) : undefined,
    }))}
  />
)}
```

No new imports — `RelatedRail` is already imported by Task 14.

- [ ] **Step 4: Manual verification**

Run: `cd web && pnpm dev`. Open any audiobook detail page — the rail should NOT appear (backend doesn't ship the array yet). To smoke-test the render path locally, temporarily mock the hook return to include three `similar_audiobooks` entries; revert before commit. Confirm:
- Heading reads "Similar audiobooks".
- Subtitle reads "Based on listening patterns".
- Rail renders above any "Also by author" / "In series" rails when both are present.

- [ ] **Step 5: Lint + commit**

```bash
cd web && pnpm run lint && pnpm run format:check
```

```bash
git add web/src/lib/audiobooks/types.ts \
        web/src/pages/audiobooks/components/RelatedRail.tsx \
        web/src/pages/audiobooks/AudiobookDetail.tsx
git commit -m "feat(audiobooks): add embedding-based Similar audiobooks rail"
```

---

## Final verification

- [ ] **Run the full frontend test suite:**

```bash
cd web && pnpm test -- --run
```
Expected: all PASS, no regressions in non-audiobook components.

- [ ] **Lint + format:**

```bash
cd web && pnpm run lint && pnpm run format:check
```
Expected: clean.

- [ ] **TypeScript build:**

```bash
cd web && pnpm run build
```
Expected: clean (the build step runs `tsc -b` first).

- [ ] **Manual smoke test (end-to-end):**

Run `cd web && pnpm dev`. With an audiobook that has chapters + progress + a narrator:

1. Open the detail page.
   - Hero shows progress bar above buttons.
   - Resume button reads "Resume · <chapter name>".
   - Chapters section is expanded, current chapter is highlighted.
   - Narrator card appears.
2. Click Resume. Mini bar appears at the bottom.
   - Chapter title visible in the left column.
   - SpeedMenu is a popover (not a native select).
   - SleepTimerMenu is a popover; arming "5 min" shows a countdown on the trigger.
3. Click the cover tile in the mini bar.
   - Now Listening overlay appears, full-screen.
   - Cover, author, narrator, current-chapter heading all visible.
   - Tap the right time toggles between total and remaining.
4. Use the chevron-down to collapse.
   - Returns to mini bar; `audio.currentTime` continues monotonically (no restart).
5. Close the player with the X. State resets.

If anything diverges, treat the divergence as a bug in the task that produced it and fix in a follow-up commit on the same branch.
