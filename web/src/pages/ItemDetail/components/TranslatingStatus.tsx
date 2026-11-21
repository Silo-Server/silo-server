import { Languages } from "lucide-react";

/**
 * The status shown while an on-view translation runs. Only the icon pulses:
 * the description it refers to stays at full strength, because faded body
 * text falls below WCAG AA contrast. It takes the Translate button's height
 * (a transparent border and the same vertical padding), so swapping one for
 * the other doesn't move the content below.
 */
export default function TranslatingStatus({
  label,
  className = "",
}: {
  label: string;
  className?: string;
}) {
  return (
    <span
      className={`text-foreground/80 inline-flex items-center gap-1.5 border-y border-transparent py-0.5 text-xs ${className}`}
    >
      <Languages className="h-3 w-3 motion-safe:animate-pulse" aria-hidden="true" />
      {label}
    </span>
  );
}
