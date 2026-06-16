import { EyeOff } from "lucide-react";

import ItemGrid from "@/components/ItemGrid";
import { useHiddenList } from "@/hooks/queries/hidden";
import { useDocumentTitle } from "@/hooks/useDocumentTitle";

export default function Hidden() {
  useDocumentTitle("Not Interested");
  const { data: items, isLoading } = useHiddenList();

  const hasItems = (items?.length ?? 0) > 0;

  return (
    <div className="page-shell space-y-6 py-4 sm:py-6">
      <header className="space-y-2">
        <h1 className="page-title text-[clamp(2rem,5vw,3.5rem)]">Not Interested</h1>
        <p className="page-subtitle text-sm sm:text-base">
          Titles hidden from your recommendations and discovery rows. Use the item menu to show a
          title in recommendations again.
        </p>
      </header>

      {!isLoading && !hasItems ? (
        <section className="flex min-h-[40dvh] flex-col items-center justify-center py-16 text-center">
          <div className="text-muted-foreground mb-6">
            <EyeOff className="h-10 w-10" strokeWidth={1.5} />
          </div>
          <p className="page-subtitle max-w-xl text-sm sm:text-base">
            You haven't marked anything as "Not Interested" yet.
          </p>
        </section>
      ) : (
        <ItemGrid items={items ?? []} loading={isLoading} />
      )}
    </div>
  );
}
