import { useState, useCallback } from "react";
import { Star } from "lucide-react";

interface StarRatingProps {
  value: number | null;
  onChange: (rating: number | null) => void;
  size?: number;
}

const STAR_COUNT = 5;

export default function StarRating({ value, onChange, size = 20 }: StarRatingProps) {
  const [hoverValue, setHoverValue] = useState<number | null>(null);

  const displayValue = hoverValue ?? value;

  function handleMouseEnter(star: number) {
    setHoverValue(star);
  }

  function handleMouseLeave() {
    setHoverValue(null);
  }

  function handleClick(star: number) {
    if (star === value) {
      onChange(null);
    } else {
      onChange(star);
    }
  }

  const handleKeyDown = useCallback(
    (e: React.KeyboardEvent) => {
      let newValue: number | null = null;
      if (e.key === "ArrowRight" || e.key === "ArrowUp") {
        e.preventDefault();
        newValue = Math.min((value ?? 0) + 1, STAR_COUNT);
      } else if (e.key === "ArrowLeft" || e.key === "ArrowDown") {
        e.preventDefault();
        newValue = Math.max((value ?? 2) - 1, 1);
      }
      if (newValue !== null) {
        onChange(newValue);
      }
    },
    [value, onChange],
  );

  const tabbableStar = value ?? 1;

  return (
    <div
      role="radiogroup"
      aria-label="Rating"
      className="glass-subtle flex items-center gap-0.5 rounded-full px-2.5 py-2"
      onMouseLeave={handleMouseLeave}
      onKeyDown={handleKeyDown}
    >
      {Array.from({ length: STAR_COUNT }, (_, i) => {
        const star = i + 1;
        const filled = displayValue !== null && star <= displayValue;
        return (
          <button
            key={star}
            type="button"
            role="radio"
            aria-label={`${star} star${star !== 1 ? "s" : ""}`}
            aria-checked={value === star}
            tabIndex={star === tabbableStar ? 0 : -1}
            className={`transform-gpu cursor-pointer border-none bg-transparent p-0.5 leading-none transition-[color,scale] duration-100 ease-out motion-safe:hover:scale-110 motion-reduce:transition-none ${filled ? "text-yellow-400" : "text-muted-foreground/50"}`}
            onMouseEnter={() => handleMouseEnter(star)}
            onClick={() => handleClick(star)}
          >
            <Star
              size={size}
              fill="currentColor"
              fillOpacity={filled ? 1 : 0}
              strokeWidth={1.5}
              className="transition-[fill-opacity] duration-100 ease-out motion-reduce:transition-none"
            />
          </button>
        );
      })}
    </div>
  );
}
