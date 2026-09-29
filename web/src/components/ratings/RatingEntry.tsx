// A separate file rather than an inlined data URI, so the logo stays out of
// the launch bundle.
import tmdbLogo from "@/assets/tmdb/tmdb-logo.svg?no-inline";
import { cn } from "@/lib/utils";

import type { DisplayRating } from "./ratings";

interface RatingEntryProps {
  rating: DisplayRating;
  size?: "sm" | "md";
  className?: string;
}

/**
 * A rating as its source's mark and its score: "IMDb 8.5", "RT 93%". Marks
 * are plain text, never the source's logo artwork, except TMDB's approved
 * logo, which TMDB's terms allow.
 */
export function RatingEntry({ rating, size = "md", className }: RatingEntryProps) {
  return (
    <span className={cn("inline-flex items-center gap-1.5 whitespace-nowrap", className)}>
      {rating.source === "tmdb" ? (
        <img
          src={tmdbLogo}
          alt={rating.name}
          className={cn("w-auto shrink-0", size === "sm" ? "h-2" : "h-2.5")}
        />
      ) : (
        <span
          className={cn(
            "font-semibold tracking-tight opacity-75",
            size === "sm" ? "text-xs" : "text-[13px]",
          )}
        >
          {rating.name}
        </span>
      )}{" "}
      <span className={cn("font-bold tabular-nums", size === "sm" ? "text-sm" : "text-[15px]")}>
        {rating.display}
      </span>
    </span>
  );
}
