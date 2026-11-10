import { X } from "lucide-react";

import type { GuidedFormState } from "@/components/collections/CollectionGuidedRulesEditor";
import { Badge } from "@/components/ui/badge";
import { useT } from "@/i18n";

import type { ActiveFilterBadge } from "./catalogFilterBadges";

interface ActiveFilterBadgesProps {
  badges: ActiveFilterBadge[];
  onClear: (patch: Partial<GuidedFormState>) => void;
}

export default function ActiveFilterBadges({ badges, onClear }: ActiveFilterBadgesProps) {
  const t = useT();
  if (badges.length === 0) return null;

  return (
    <div className="flex flex-wrap items-center gap-2">
      {badges.map((badge) => (
        <Badge key={badge.key} variant="secondary" className="gap-1 pe-1">
          {badge.label}
          <button
            type="button"
            onClick={() => onClear(badge.clearPatch)}
            className="hover:bg-muted ms-0.5 rounded-sm p-0.5"
          >
            <X className="h-3 w-3" />
            <span className="sr-only">
              {t("web.catalog.filters.removeBadge", { filter: badge.label })}
            </span>
          </button>
        </Badge>
      ))}
    </div>
  );
}
