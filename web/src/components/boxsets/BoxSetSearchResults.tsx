import type { BoxSetSummary } from "@/api/types";
import BoxSetCard from "@/components/boxsets/BoxSetCard";
import { useUICustomization } from "@/hooks/useUICustomization";
import { cardGridClasses } from "@/lib/uiCustomization";

/**
 * The box sets matching a search, ahead of the title grid. They use the
 * grid's own columns, so a box set lines up with the movies listed after it.
 */
export default function BoxSetSearchResults({ boxSets }: { boxSets: BoxSetSummary[] }) {
  const { cardPresentation } = useUICustomization();
  return (
    <section aria-label="Box sets" className="space-y-3">
      <h2 className="text-lg font-semibold">Box sets</h2>
      <div className={cardGridClasses(cardPresentation.poster_size)}>
        {boxSets.map((boxSet) => (
          <BoxSetCard key={`${boxSet.library_id}:${boxSet.id}`} boxSet={boxSet} />
        ))}
      </div>
    </section>
  );
}
