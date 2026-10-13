import { Button } from "@/components/ui/button";
import { rowKindGroup } from "@/lib/homeRows/catalog";
import { cn } from "@/lib/utils";
import { GROUP_ICONS, GROUP_TINTS, KIND_ICONS } from "../rowIcons";

/** Edit row's "Shows" line: what the row is, with Change when it may change. */
export function ShowsSummary({
  sectionType,
  label,
  sentence,
  onChange,
}: {
  sectionType: string;
  label: string;
  sentence: string;
  /** Omitted when the kind cannot change (legacy Trakt rows). */
  onChange?: () => void;
}) {
  const group = rowKindGroup(sectionType);
  const Icon = KIND_ICONS[sectionType] ?? GROUP_ICONS[group];
  return (
    <div className="grid gap-2">
      <span className="text-sm font-medium">Shows</span>
      <div className="border-border flex items-center gap-3 rounded-xl border py-2.5 pr-3 pl-2.5">
        <span
          aria-hidden
          className={cn(
            "grid size-[34px] shrink-0 place-items-center rounded-[9px] ring-1 ring-inset",
            GROUP_TINTS[group],
          )}
        >
          <Icon className="size-[17px]" />
        </span>
        <span className="min-w-0 flex-1">
          <span className="block truncate text-sm font-semibold">{label}</span>
          <span className="text-muted-foreground block truncate text-[12.5px]">{sentence}</span>
        </span>
        {onChange ? (
          <Button
            type="button"
            size="sm"
            variant="outline"
            aria-label={`Change what ${label} shows`}
            onClick={onChange}
          >
            Change
          </Button>
        ) : null}
      </div>
    </div>
  );
}
