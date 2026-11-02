import { useId } from "react";
import { RadioCardItem, RadioGroup } from "@/components/ui/radio-group";
import { variantFamily, variantOf } from "@/lib/homeRows/variants";
import type { GalleryPreset } from "@/lib/recipes";
import { cn } from "@/lib/utils";

const COLUMNS: Record<number, string> = {
  2: "sm:grid-cols-2",
  3: "sm:grid-cols-3",
  4: "grid-cols-2 sm:grid-cols-4",
};

/**
 * Radio cards for the presets of one row kind. A config that matches no
 * preset shows none selected; nothing changes until the user picks one.
 * A card without its own hint shows the server's description of the preset
 * (a mood's genres, rating floor and vote minimum).
 */
export function VariantChoice({
  sectionType,
  config,
  presets,
  onChange,
}: {
  sectionType: string;
  config: Record<string, unknown>;
  presets?: readonly GalleryPreset[];
  onChange: (presetKey: string) => void;
}) {
  const labelId = useId();
  const family = variantFamily(sectionType);
  if (!family) return null;
  return (
    <div className="grid gap-2">
      <span id={labelId} className="text-sm font-medium">
        {family.control}
      </span>
      <RadioGroup
        aria-labelledby={labelId}
        value={variantOf(sectionType, config) ?? ""}
        onValueChange={onChange}
        className={cn("gap-2.5", COLUMNS[family.options.length] ?? "grid-cols-2 sm:grid-cols-4")}
      >
        {family.options.map((option) => (
          <RadioCardItem
            key={option.presetKey}
            value={option.presetKey}
            label={option.label}
            hint={
              option.hint ??
              presets
                ?.find((preset) => preset.key === option.presetKey)
                ?.description_short.replace(/\.$/, "")
            }
          />
        ))}
      </RadioGroup>
    </div>
  );
}
