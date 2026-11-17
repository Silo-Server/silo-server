import { memo, useRef, useState } from "react";
import { Star } from "lucide-react";
import { readFinePointer } from "@/lib/pointerCapability";
import { useT } from "@/i18n";
import { readingArrowKey } from "@/lib/direction";

interface StarRatingProps {
  /** The rating score, 1 to 10: two per star, so 7 is three and a half stars. */
  value: number | null;
  onChange: (score: number | null) => void;
  size?: number;
  /** When false, ArrowUp/ArrowDown are left to an enclosing menu's navigation. */
  verticalArrows?: boolean;
}

const STAR_COUNT = 5;
const MAX_SCORE = STAR_COUNT * 2;

/**
 * How far a press must move sideways before it counts as a drag. A finger
 * lifting off the middle of a star drifts a pixel or two, which can cross into
 * the star's other half; that is still a tap and fills the whole star.
 */
const DRAG_THRESHOLD_PX = 6;

/** The score of the whole star a half belongs to: 7 and 8 are both 8. */
function wholeStar(score: number): number {
  return Math.ceil(score / 2) * 2;
}

/**
 * A five-star rating radio group with half stars: each star is two radios,
 * its left half for the odd score and its right half for the even one, so the
 * scores run from 1 (half a star) to 10. A click or tap on either half fills
 * the whole star; on the star that is already set it clears the rating
 * (onChange(null)). Half stars take a drag: pressing and moving sideways
 * fills the stars up to the half under the pointer and sets that value on
 * release. Keyboard users step half a star with the arrow keys, and
 * activating a radio sets its own value. Hovering previews the whole star in
 * CSS only (see `.star-rating` in app.css), so moving across the stars does
 * not render.
 */
function StarRating({ value, onChange, size = 20, verticalArrows = true }: StarRatingProps) {
  const t = useT();
  const [suppressPreview, setSuppressPreview] = useState(false);
  // The score under a pressed pointer while it drags, null when none is down.
  const [dragScore, setDragScore] = useState<number | null>(null);
  const drag = useRef<{
    pointerId: number;
    startX: number;
    startScore: number;
    moved: boolean;
  } | null>(null);
  // Set when a press already answered the click its release produces.
  const pointerHandled = useRef(false);
  // The value just picked, shown until the parent passes a new value: the
  // parent applies the change a moment later, and the old value would flash
  // back in meanwhile. Any new value, including a failed write's rollback,
  // replaces it.
  const [picked, setPicked] = useState<{ score: number | null } | null>(null);
  const [lastValue, setLastValue] = useState(value);
  if (lastValue !== value) {
    setLastValue(value);
    setPicked(null);
  }
  const rating = picked ? picked.score : value;

  function commit(score: number | null) {
    setPicked({ score });
    onChange(score);
  }

  /** Fills the star a half belongs to, or clears the rating when it is set. */
  function toggleStar(score: number) {
    const whole = wholeStar(score);
    commit(whole === rating ? null : whole);
  }

  /**
   * Fills the clicked star, or clears the rating when that star is the one
   * set. A keyboard activation (detail 0) sets the radio's own score instead,
   * so a screen reader user gets the half star the radio announces.
   *
   * The CSS hover preview fills up to the hovered star whatever the stored
   * value is, so after a click it would keep drawing the old rating under a
   * pointer that has not moved. A pointer click therefore suppresses the
   * preview until the pointer leaves that star. Keyboard activation fires a
   * click with detail 0 and has no pointer to leave, so it never sets the flag.
   * Neither does a touch tap: its pointerleave fires before the click, and the
   * preview is already off once a touch press clears data-fine-pointer.
   */
  function handleClick(event: React.MouseEvent, score: number) {
    if (event.detail !== 0 && pointerHandled.current) {
      pointerHandled.current = false;
      return;
    }
    if (event.detail === 0) {
      commit(score === rating ? null : score);
      return;
    }
    if (readFinePointer() !== false) setSuppressPreview(true);
    toggleStar(score);
  }

  /**
   * Ends the hold a pointer click puts on the hover preview once the pointer
   * leaves the star. Moving between a star's two halves leaves one radio but
   * not the star, so it keeps the hold.
   */
  function handleHalfPointerLeave(event: React.PointerEvent<HTMLButtonElement>, score: number) {
    const half = event.currentTarget;
    const otherHalf = score % 2 === 0 ? half.previousElementSibling : half.nextElementSibling;
    if (event.relatedTarget instanceof Node && otherHalf?.contains(event.relatedTarget)) return;
    setSuppressPreview(false);
  }

  function handleKeyDown(e: React.KeyboardEvent) {
    let newValue: number | null = null;
    const key = readingArrowKey(e);
    if (key === "ArrowRight" || (verticalArrows && key === "ArrowUp")) {
      e.preventDefault();
      newValue = Math.min((rating ?? 0) + 1, MAX_SCORE);
    } else if (key === "ArrowLeft" || (verticalArrows && key === "ArrowDown")) {
      e.preventDefault();
      newValue = Math.max((rating ?? 2) - 1, 1);
    }
    if (newValue !== null) {
      commit(newValue);
      e.currentTarget
        .querySelector<HTMLButtonElement>(`[data-rating="${newValue}"]`)
        ?.focus({ preventScroll: true });
    }
  }

  /**
   * The score of the half star horizontally nearest the pointer, so a drag
   * that wanders above or below the stars, or past either end, still picks
   * one. Reading the laid-out halves keeps it right in either text direction.
   */
  function scoreAt(group: HTMLElement, clientX: number): number | null {
    let best: number | null = null;
    let bestDistance = Infinity;
    for (const half of group.querySelectorAll<HTMLElement>("[data-rating]")) {
      const rect = half.getBoundingClientRect();
      const distance =
        clientX < rect.left ? rect.left - clientX : Math.max(0, clientX - rect.right);
      if (distance < bestDistance) {
        bestDistance = distance;
        best = Number(half.dataset.rating);
      }
    }
    return best;
  }

  function handlePointerDown(event: React.PointerEvent<HTMLDivElement>) {
    if (event.pointerType === "mouse" && event.button !== 0) return;
    pointerHandled.current = false;
    const score = scoreAt(event.currentTarget, event.clientX);
    if (score === null) return;
    event.currentTarget.setPointerCapture?.(event.pointerId);
    drag.current = {
      pointerId: event.pointerId,
      startX: event.clientX,
      startScore: score,
      moved: false,
    };
    setDragScore(wholeStar(score));
  }

  function handlePointerMove(event: React.PointerEvent<HTMLDivElement>) {
    if (drag.current?.pointerId !== event.pointerId) return;
    const score = scoreAt(event.currentTarget, event.clientX);
    if (score === null) return;
    if (Math.abs(event.clientX - drag.current.startX) >= DRAG_THRESHOLD_PX) {
      drag.current.moved = true;
    }
    if (drag.current.moved) setDragScore(score);
  }

  /**
   * Sets the dragged-to value on release. A press that never left its half
   * is a tap and behaves as a click: it fills the whole star, or clears the
   * rating when that star is the one set.
   */
  function handlePointerUp(event: React.PointerEvent<HTMLDivElement>) {
    const current = drag.current;
    if (current?.pointerId !== event.pointerId) return;
    drag.current = null;
    setDragScore(null);
    pointerHandled.current = true;
    if (event.pointerType !== "touch" && readFinePointer() !== false) setSuppressPreview(true);
    if (current.moved) {
      // Read the release point: the last move may not have rendered yet.
      commit(scoreAt(event.currentTarget, event.clientX) ?? current.startScore);
    } else {
      toggleStar(current.startScore);
    }
  }

  function handlePointerCancel(event: React.PointerEvent<HTMLDivElement>) {
    if (drag.current?.pointerId !== event.pointerId) return;
    drag.current = null;
    setDragScore(null);
  }

  const shown = dragScore ?? rating;
  const tabbableScore = rating ?? 1;
  const halfSize = size / 2;

  return (
    <div
      role="radiogroup"
      aria-label={t("web.item.rating.label")}
      className="star-rating flex touch-pan-y items-center rounded-full px-2.5 py-2 select-none"
      data-suppress-preview={suppressPreview || dragScore !== null || undefined}
      onKeyDown={handleKeyDown}
      onPointerDown={handlePointerDown}
      onPointerMove={handlePointerMove}
      onPointerUp={handlePointerUp}
      onPointerCancel={handlePointerCancel}
    >
      {Array.from({ length: MAX_SCORE }, (_, i) => {
        const score = i + 1;
        const rightHalf = score % 2 === 0;
        const current = rating === score;
        // A pointer click here clears the rating: this half's star is the one set.
        const clears = rating !== null && wholeStar(score) === rating;
        return (
          <button
            key={score}
            type="button"
            role="radio"
            aria-label={t("web.item.rating.stars", { count: score / 2 })}
            aria-checked={current}
            title={clears ? t("web.item.rating.remove") : undefined}
            tabIndex={score === tabbableScore ? 0 : -1}
            data-filled={shown !== null && score <= shown}
            data-rating={score}
            data-half={rightHalf ? "right" : "left"}
            data-clears={clears || undefined}
            className={`star-rating-star focus-visible:ring-ring cursor-pointer rounded-sm border-none bg-transparent py-0.5 leading-none outline-none focus-visible:ring-2 ${rightHalf ? "pe-[3px]" : "ps-[3px]"}`}
            onClick={(event) => handleClick(event, score)}
            onPointerLeave={(event) => handleHalfPointerLeave(event, score)}
          >
            {/* Each half shows its side of one star icon. */}
            <span className="block overflow-hidden" style={{ width: halfSize }}>
              <Star
                size={size}
                fill="none"
                strokeWidth={1.5}
                className="block"
                style={rightHalf ? { marginInlineStart: -halfSize } : undefined}
              />
            </span>
          </button>
        );
      })}
    </div>
  );
}

export default memo(StarRating);
