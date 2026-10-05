import type { PickerCard } from "@/lib/homeRows/catalog";
import { variantFamily } from "@/lib/homeRows/variants";

const MAX_CHIPS = 3;

/** A card's variants as buttons; each opens step 2 with that variant picked. */
export function VariantChips({
  card,
  onPick,
}: {
  card: PickerCard;
  onPick: (presetKey: string) => void;
}) {
  const options = variantFamily(card.type)?.options ?? [];
  if (options.length < 2) return null;
  const hidden = options.length - MAX_CHIPS;
  return (
    <span className="relative z-10 mt-2.5 flex flex-wrap gap-1.5">
      {options.slice(0, MAX_CHIPS).map((option) => (
        <button
          key={option.presetKey}
          type="button"
          aria-label={`${card.label}, ${option.chip}`}
          onClick={() => onPick(option.presetKey)}
          className="border-border bg-background/60 text-foreground/80 hover:bg-secondary hover:text-foreground focus-visible:ring-ring/50 inline-flex h-[26px] items-center rounded-full border px-2.5 text-[12.5px] font-medium whitespace-nowrap outline-none focus-visible:ring-[3px]"
        >
          {option.chip}
        </button>
      ))}
      {hidden > 0 ? (
        <span className="border-border text-muted-foreground inline-flex h-[26px] items-center rounded-full border border-dashed px-2.5 text-[12.5px] font-medium">
          +{hidden}
        </span>
      ) : null}
    </span>
  );
}
