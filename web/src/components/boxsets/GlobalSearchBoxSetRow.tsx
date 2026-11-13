import type { BoxSetSummary } from "@/api/types";
import { EnterKeyHint } from "@/components/ui/kbd";
import { useImageLoaded } from "@/hooks/useImageLoaded";
import { boxSetFilmCount, boxSetYears } from "@/lib/boxSets";
import { cn } from "@/lib/utils";
import { useAppLanguage, useT } from "@/i18n";

/** A box set option in the global search list; the list owns the layout. */
export function GlobalSearchBoxSetRow({
  boxSet,
  optionId,
  rowClassName,
  posterClassName,
  isSelected,
  onSelect,
  onPick,
}: {
  boxSet: BoxSetSummary;
  optionId: string;
  rowClassName: string;
  posterClassName: string;
  isSelected: boolean;
  onSelect: () => void;
  onPick: (boxSet: BoxSetSummary) => void;
}) {
  const t = useT();
  // The film count comes from a plain helper; follow language changes.
  useAppLanguage();
  const { loaded, onLoad, onError } = useImageLoaded(boxSet.poster_url);
  const detail = [boxSetFilmCount(boxSet.movie_count), boxSetYears(boxSet)]
    .filter(Boolean)
    .join(" · ");
  return (
    <div
      id={optionId}
      role="option"
      aria-selected={isSelected}
      aria-label={t("web.detail.boxSet.searchOptionLabel", { title: boxSet.title, detail })}
      data-selected={isSelected || undefined}
      onMouseMove={isSelected ? undefined : onSelect}
      onClick={() => onPick(boxSet)}
      className={cn(rowClassName, "data-[selected]:bg-accent rounded-md transition-colors")}
    >
      {/* Two offset sleeves behind the poster mark it as a stack of films. */}
      <div className={cn(posterClassName, "me-1.5 mt-1.5")}>
        <span
          aria-hidden
          className="bg-surface-raised border-border absolute start-1.5 -top-1.5 h-full w-full rounded-md border"
        />
        <span
          aria-hidden
          className="bg-surface-hover border-border absolute start-0.75 -top-0.75 h-full w-full rounded-md border"
        />
        <div className="bg-muted relative h-full w-full overflow-hidden rounded-md">
          {boxSet.poster_url ? (
            <img
              src={boxSet.poster_url}
              alt=""
              className={`h-full w-full object-cover ${loaded ? "opacity-100" : "opacity-0"}`}
              loading="lazy"
              onLoad={onLoad}
              onError={onError}
            />
          ) : null}
        </div>
      </div>
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          <span className="truncate text-sm font-medium">{boxSet.title}</span>
          <span className="border-border text-muted-foreground shrink-0 rounded-full border px-1.5 py-px text-[0.5625rem] font-semibold tracking-[0.12em] uppercase">
            {t("web.detail.boxSet.label")}
          </span>
        </div>
        <div className="text-muted-foreground text-xs">{detail}</div>
      </div>
      {isSelected && <EnterKeyHint />}
    </div>
  );
}
