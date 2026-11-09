import { memo, useCallback, useState } from "react";
import { Star } from "lucide-react";
import { readFinePointer } from "@/lib/pointerCapability";

interface StarRatingProps {
  value: number | null;
  onChange: (rating: number | null) => void;
  size?: number;
  /** When false, ArrowUp/ArrowDown are left to an enclosing menu's navigation. */
  verticalArrows?: boolean;
}

const STAR_COUNT = 5;

/**
 * A five-star rating radio group. Clicking a star sets that rating; clicking
 * the star that is already set clears it (onChange(null)). Arrow keys step the
 * rating. Hovering previews a value in CSS only (see `.star-rating` in
 * app.css), so moving across the stars does not render.
 */
function StarRating({ value, onChange, size = 20, verticalArrows = true }: StarRatingProps) {
  const [suppressPreview, setSuppressPreview] = useState(false);
  const clearSuppressPreview = useCallback(() => setSuppressPreview(false), []);

  /**
   * Sets the clicked star, or clears the rating when it is the current one.
   *
   * The CSS hover preview fills up to the hovered star whatever the stored
   * value is, so after a click it would keep drawing the old rating under a
   * pointer that has not moved. A pointer click therefore suppresses the
   * preview until the pointer leaves that star. Keyboard activation fires a
   * click with detail 0 and has no pointer to leave, so it never sets the flag.
   * Neither does a touch tap: its pointerleave fires before the click, and the
   * preview is already off once a touch press clears data-fine-pointer.
   */
  function handleClick(event: React.MouseEvent, star: number) {
    if (event.detail !== 0 && readFinePointer() !== false) setSuppressPreview(true);
    if (star === value) {
      onChange(null);
    } else {
      onChange(star);
    }
  }

  const handleKeyDown = useCallback(
    (e: React.KeyboardEvent) => {
      let newValue: number | null = null;
      if (e.key === "ArrowRight" || (verticalArrows && e.key === "ArrowUp")) {
        e.preventDefault();
        newValue = Math.min((value ?? 0) + 1, STAR_COUNT);
      } else if (e.key === "ArrowLeft" || (verticalArrows && e.key === "ArrowDown")) {
        e.preventDefault();
        newValue = Math.max((value ?? 2) - 1, 1);
      }
      if (newValue !== null) {
        onChange(newValue);
        e.currentTarget
          .querySelector<HTMLButtonElement>(`[data-rating="${newValue}"]`)
          ?.focus({ preventScroll: true });
      }
    },
    [value, onChange, verticalArrows],
  );

  const tabbableStar = value ?? 1;

  return (
    <div
      role="radiogroup"
      aria-label="Rating"
      className="star-rating flex items-center gap-0.5 rounded-full px-2.5 py-2"
      data-suppress-preview={suppressPreview || undefined}
      onKeyDown={handleKeyDown}
    >
      {Array.from({ length: STAR_COUNT }, (_, i) => {
        const star = i + 1;
        const filled = value !== null && star <= value;
        const current = value === star;
        return (
          <button
            key={star}
            type="button"
            role="radio"
            aria-label={`${star} star${star !== 1 ? "s" : ""}`}
            aria-checked={current}
            title={current ? "Remove rating" : undefined}
            tabIndex={star === tabbableStar ? 0 : -1}
            data-filled={filled}
            data-rating={star}
            className="star-rating-star focus-visible:ring-ring cursor-pointer rounded-sm border-none bg-transparent p-0.5 leading-none outline-none focus-visible:ring-2"
            onClick={(event) => handleClick(event, star)}
            onPointerLeave={clearSuppressPreview}
          >
            <Star size={size} fill="none" strokeWidth={1.5} />
          </button>
        );
      })}
    </div>
  );
}

export default memo(StarRating);
