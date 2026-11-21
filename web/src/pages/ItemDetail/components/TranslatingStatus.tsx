import type { ReactNode } from "react";
import { Languages } from "lucide-react";

/**
 * The status of an on-view translation, as a live region that stays mounted
 * so screen readers announce it: the label while the translation runs, then
 * any `children` (the "Translated by AI" label) once it ends. A region that
 * mounts with its text is not announced reliably. While empty it is taken
 * out of the layout so it adds no gap.
 *
 * Only the icon pulses: the description it refers to stays at full strength,
 * because faded body text falls below WCAG AA contrast. With `matchButton`,
 * the label takes the Translate button's height (a transparent border and the
 * same vertical padding), so swapping one for the other doesn't move the
 * content below.
 */
export default function TranslatingStatus({
  translating,
  label,
  matchButton = true,
  className = "",
  children,
}: {
  translating: boolean;
  label: string;
  /** Take the Translate button's height; off where no button precedes it. */
  matchButton?: boolean;
  className?: string;
  children?: ReactNode;
}) {
  return (
    <span role="status" className={`inline-flex empty:absolute ${className}`}>
      {translating ? (
        <span
          className={`text-foreground/80 inline-flex items-center gap-1.5 text-xs ${
            matchButton ? "border-y border-transparent py-0.5" : ""
          }`}
        >
          <Languages className="h-3 w-3 motion-safe:animate-pulse" aria-hidden="true" />
          {label}
        </span>
      ) : (
        children
      )}
    </span>
  );
}
