import { useMemo, useRef, useState, type KeyboardEvent, type PointerEvent } from "react";

import type { CatalogLetter } from "@/hooks/queries/catalog";
import { cn } from "@/lib/utils";

import { LETTER_BAR_LETTERS, spokenLetter, titleCount } from "./letterIndex";

export interface LetterBarProps {
  /**
   * Letters with titles, from the letter index. Letters missing here are
   * greyed out; undefined while the index loads, which greys them all.
   */
  letters: readonly CatalogLetter[] | undefined;
  /** The letter the top of the list is in, marked as the current one. */
  activeLetter?: string;
  onJump: (letter: CatalogLetter) => void;
}

/**
 * A–Z jump bar for a title-ordered grid, pinned to the right edge like a
 * phone's contacts index. One tab stop: arrow keys, Home/End, or typing a
 * letter move between letters that have titles, Enter or Space jumps. On a
 * touchscreen, sliding a finger along the bar jumps as it passes each letter.
 * While the index loads, the bar holds its place with every letter greyed, so
 * it doesn't pop in after the grid.
 */
export default function LetterBar({ letters, activeLetter, onJump }: LetterBarProps) {
  const loading = letters === undefined;
  const byLetter = useMemo(
    () => new Map((letters ?? []).map((letter) => [letter.letter, letter])),
    [letters],
  );
  const enabled = useMemo(
    () => LETTER_BAR_LETTERS.filter((letter) => byLetter.has(letter)),
    [byLetter],
  );
  const [focused, setFocused] = useState<string>();
  const [announcement, setAnnouncement] = useState("");
  const [scrubbing, setScrubbing] = useState<string>();
  const buttonRefs = useRef(new Map<string, HTMLButtonElement>());
  const scrubActive = useRef(false);
  const lastScrubbed = useRef<string | undefined>(undefined);

  // The roving tab stop: the letter last focused, else the current one, else
  // the first letter with titles.
  const tabStop = [focused, activeLetter, enabled[0]].find(
    (letter) => letter !== undefined && byLetter.has(letter),
  );

  const jump = (letter: string) => {
    const target = byLetter.get(letter);
    if (!target) return;
    onJump(target);
    setAnnouncement(`Showing titles under ${spokenLetter(letter)}`);
  };

  const focusLetter = (letter: string | undefined) => {
    if (!letter) return;
    setFocused(letter);
    buttonRefs.current.get(letter)?.focus();
  };

  const onKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    if (enabled.length === 0) return;
    const at = enabled.indexOf(tabStop ?? "");
    let next: string | undefined;
    switch (event.key) {
      case "ArrowDown":
      case "ArrowRight":
        next = enabled[Math.min(at + 1, enabled.length - 1)];
        break;
      case "ArrowUp":
      case "ArrowLeft":
        next = enabled[Math.max(at - 1, 0)];
        break;
      case "Home":
        next = enabled[0];
        break;
      case "End":
        next = enabled[enabled.length - 1];
        break;
      default: {
        // Space activates the focused letter like Enter; other printable
        // keys move to their letter, with digits and symbols on #.
        if (!/^\S$/u.test(event.key) || event.metaKey || event.ctrlKey || event.altKey) return;
        const typed = /[a-z]/i.test(event.key) ? event.key.toUpperCase() : "#";
        if (!byLetter.has(typed)) return;
        next = typed;
      }
    }
    event.preventDefault();
    focusLetter(next);
  };

  // Touch and pen scrub along the bar; a mouse uses ordinary clicks.
  const scrubTo = (event: PointerEvent<HTMLDivElement>) => {
    const hit = document
      .elementFromPoint(event.clientX, event.clientY)
      ?.closest<HTMLElement>("[data-letter]");
    const letter = hit?.dataset.letter;
    if (!letter || !byLetter.has(letter) || letter === lastScrubbed.current) return;
    lastScrubbed.current = letter;
    setScrubbing(letter);
    jump(letter);
  };
  const endScrub = () => {
    scrubActive.current = false;
    lastScrubbed.current = undefined;
    setScrubbing(undefined);
  };

  return (
    <div
      role="toolbar"
      aria-label="Jump to letter"
      aria-orientation="vertical"
      aria-busy={loading || undefined}
      onKeyDown={onKeyDown}
      onPointerDown={(event) => {
        if (event.pointerType === "mouse") return;
        event.currentTarget.setPointerCapture(event.pointerId);
        scrubActive.current = true;
        scrubTo(event);
      }}
      onPointerMove={(event) => {
        if (scrubActive.current) scrubTo(event);
      }}
      onPointerUp={endScrub}
      onPointerCancel={endScrub}
      className={cn(
        "surface-panel fixed right-1 z-30 flex -translate-y-1/2 touch-none flex-col items-stretch rounded-full border py-1.5 select-none sm:right-2",
        // Centered a little low and short of the viewport, so it clears the
        // page tabs at the top and the scroll-to-top button at the bottom.
        "top-[calc(50%+2rem)] h-[min(42rem,calc(100dvh-14rem))]",
        // A short viewport (a phone on its side) can't fit 27 readable rows,
        // so the letters wrap into two columns of 14 in the same order.
        "[@media(max-height:599px)]:top-[calc(50%+0.5rem)] [@media(max-height:599px)]:grid [@media(max-height:599px)]:h-[calc(100dvh-9rem)] [@media(max-height:599px)]:grid-flow-col [@media(max-height:599px)]:grid-rows-[repeat(14,minmax(0,1fr))] [@media(max-height:599px)]:rounded-2xl [@media(max-height:599px)]:px-1",
      )}
    >
      {LETTER_BAR_LETTERS.map((letter) => {
        const entry = byLetter.get(letter);
        const isActive = letter === activeLetter;
        return (
          <button
            key={letter}
            ref={(element) => {
              if (element) buttonRefs.current.set(letter, element);
              else buttonRefs.current.delete(letter);
            }}
            type="button"
            data-letter={letter}
            tabIndex={letter === tabStop ? 0 : -1}
            aria-disabled={entry ? undefined : true}
            aria-current={isActive ? "true" : undefined}
            aria-label={
              loading
                ? spokenLetter(letter)
                : `${spokenLetter(letter)}, ${entry ? titleCount(entry.count) : "no titles"}`
            }
            onFocus={() => setFocused(letter)}
            onClick={() => jump(letter)}
            className={cn(
              "focus-visible:ring-ring flex min-h-0 w-5 flex-1 items-center justify-center rounded-full text-[10px] leading-none font-semibold transition-colors focus-visible:ring-2 focus-visible:outline-none sm:w-6 sm:text-[11px]",
              !entry && "text-muted-foreground/35 cursor-default",
              entry && !isActive && "text-muted-foreground hover:text-foreground",
              isActive && "bg-primary text-primary-foreground",
            )}
          >
            {letter}
          </button>
        );
      })}
      {scrubbing ? (
        <span
          aria-hidden="true"
          className="bg-primary text-primary-foreground pointer-events-none absolute top-1/2 right-full mr-3 flex h-14 w-14 -translate-y-1/2 items-center justify-center rounded-2xl text-2xl font-semibold shadow-lg"
        >
          {scrubbing}
        </span>
      ) : null}
      <span role="status" aria-live="polite" className="sr-only">
        {announcement}
      </span>
    </div>
  );
}
