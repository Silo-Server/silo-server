import { Languages } from "lucide-react";

/**
 * The status shown while an on-view translation runs. Only the icon pulses:
 * the description it refers to stays at full strength, because faded body
 * text falls below WCAG AA contrast.
 */
export default function TranslatingStatus({
  label,
  className = "",
}: {
  label: string;
  className?: string;
}) {
  return (
    <span className={`text-foreground/80 inline-flex items-center gap-1.5 text-xs ${className}`}>
      <Languages className="h-3 w-3 motion-safe:animate-pulse" aria-hidden="true" />
      {label}
    </span>
  );
}
