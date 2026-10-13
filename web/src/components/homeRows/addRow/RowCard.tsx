import { useId } from "react";
import { Check, ChevronRight } from "lucide-react";
import type { PickerCard } from "@/lib/homeRows/catalog";
import { cn } from "@/lib/utils";
import { GROUP_ICONS, GROUP_TINTS, KIND_ICONS } from "../rowIcons";
import { variantSummary } from "@/lib/homeRows/variants";
import { VariantChips } from "./VariantChips";

/**
 * One kind of row in the picker. The whole card is one button; variant chips
 * sit above it as their own buttons, so no button is nested in another.
 */
export function RowCard({
  card,
  onPage,
  pageLabel,
  compact,
  onPick,
}: {
  card: PickerCard;
  /** A row of this kind is already on the page. */
  onPage: boolean;
  pageLabel: string;
  /** Phones: variants as text and a chevron instead of chips. */
  compact: boolean;
  onPick: (presetKey?: string) => void;
}) {
  const sentenceId = useId();
  const Icon = KIND_ICONS[card.type] ?? GROUP_ICONS[card.group];
  const summary = compact ? variantSummary(card.type) : null;
  return (
    <div
      className={cn(
        "border-border/90 bg-surface/60 hover:bg-accent focus-within:ring-ring/45 focus-within:border-foreground/50 relative grid grid-cols-[40px_minmax(0,1fr)] items-start gap-x-3.5 rounded-[14px] border py-3.5 pr-4 pl-3.5 text-left focus-within:ring-[3px]",
        compact && "pr-10",
      )}
    >
      <span
        aria-hidden
        className={cn(
          "grid size-10 place-items-center rounded-[11px] ring-1 ring-inset",
          GROUP_TINTS[card.group],
        )}
      >
        <Icon className="size-[19px]" />
      </span>
      <span className="min-w-0">
        <button
          type="button"
          aria-describedby={sentenceId}
          onClick={() => onPick()}
          className={cn(
            "flex min-h-5 items-center gap-2 text-left text-[14.5px] font-semibold tracking-[-0.01em] outline-none after:absolute after:inset-0 after:rounded-[14px]",
            onPage && "pr-24",
          )}
        >
          {card.label}
        </button>
        <span
          id={sentenceId}
          className="text-muted-foreground mt-0.5 block text-[13px] leading-[1.45]"
        >
          {card.sentence}
          {onPage ? (
            <>
              {" "}
              <span className="sr-only">Already on {pageLabel}.</span>
            </>
          ) : null}
        </span>
        {summary ? (
          <span className="text-foreground/80 mt-1 block text-[12.5px]">{summary}</span>
        ) : compact ? null : (
          <VariantChips card={card} onPick={(presetKey) => onPick(presetKey)} />
        )}
      </span>
      {onPage ? (
        <span
          aria-hidden
          className="bg-background/70 ring-border absolute top-3 right-3 inline-flex h-[22px] items-center gap-1 rounded-full px-2 text-[11.5px] font-semibold ring-1 ring-inset"
        >
          <Check className="text-success size-3" />
          On {pageLabel}
        </span>
      ) : null}
      {compact ? (
        <ChevronRight
          aria-hidden
          className="text-muted-foreground absolute top-1/2 right-3.5 size-4 -translate-y-1/2"
        />
      ) : null}
    </div>
  );
}
