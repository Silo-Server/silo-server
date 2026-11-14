import { Languages } from "lucide-react";

import { useT } from "@/i18n";

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
  const t = useT();
  const description = t("web.detail.translation.machineTranslated");
  if (compact) {
    return (
      <span
        className={`text-muted-foreground/70 inline-flex items-center ${className}`}
        title={description}
      >
        <Languages className="h-3 w-3" aria-hidden="true" />
        <span className="sr-only">{description}</span>
      </span>
    );
  }
  return (
    <span
      className={`text-muted-foreground/70 inline-flex items-center gap-1.5 text-xs ${className}`}
    >
      <Languages className="h-3 w-3" aria-hidden="true" />
      {description}
    </span>
  );
}
