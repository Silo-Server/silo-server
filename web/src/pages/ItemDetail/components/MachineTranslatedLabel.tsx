import { Languages } from "lucide-react";

const DESCRIPTION = "Translated by AI";

/**
 * Marks text the server reports as machine-translated (the
 * `machine_translated_fields` of a detail, season or episode). The compact
 * form is an icon for dense rows such as episode cards; it keeps the words
 * for screen readers and as a tooltip.
 */
export default function MachineTranslatedLabel({
  compact = false,
  className = "",
}: {
  compact?: boolean;
  className?: string;
}) {
  if (compact) {
    return (
      <span
        className={`text-muted-foreground/70 inline-flex items-center ${className}`}
        title={DESCRIPTION}
      >
        <Languages className="h-3 w-3" aria-hidden="true" />
        <span className="sr-only">{DESCRIPTION}</span>
      </span>
    );
  }
  return (
    <span
      className={`text-muted-foreground/70 inline-flex items-center gap-1.5 text-xs ${className}`}
    >
      <Languages className="h-3 w-3" aria-hidden="true" />
      {DESCRIPTION}
    </span>
  );
}
