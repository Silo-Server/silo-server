import type { QueryDefinition } from "@/api/types";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { getDefaultQuerySortOrder } from "@/lib/querySortOptions";

import { getCollectionSortOptions } from "./collectionBuilderFields";
import { useShownRatingSources } from "@/hooks/queries/ratingsCapability";

interface CollectionOrderingEditorProps {
  query: QueryDefinition;
  sortConfig: Record<string, unknown>;
  onQueryChange: (query: QueryDefinition) => void;
  onSortConfigChange: (sortConfig: Record<string, unknown>) => void;
  allowPersonalizedSorts?: boolean;
  readOnly?: boolean;
}

export default function CollectionOrderingEditor({
  query,
  sortConfig,
  onQueryChange,
  onSortConfigChange,
  allowPersonalizedSorts = false,
  readOnly = false,
}: CollectionOrderingEditorProps) {
  const orderingMode = sortConfig.mode === "manual_pins" ? "manual_pins" : "query_sort";
  const shownRatingSources = useShownRatingSources();
  const sortOptions = getCollectionSortOptions(
    allowPersonalizedSorts,
    undefined,
    shownRatingSources,
    query.sort.field,
  );

  return (
    <div className="space-y-4">
      <div className="grid gap-4 md:grid-cols-3">
        <div className="space-y-2">
          <Label>Sort By</Label>
          <Select
            value={query.sort.field}
            onValueChange={(field) =>
              onQueryChange({
                ...query,
                sort: {
                  ...query.sort,
                  field: field as QueryDefinition["sort"]["field"],
                  order: getDefaultQuerySortOrder(field),
                },
              })
            }
            disabled={readOnly}
          >
            <SelectTrigger>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {sortOptions.map((option) => (
                <SelectItem key={option.value} value={option.value}>
                  {option.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>

        <div className="space-y-2">
          <Label>Order</Label>
          <Select
            value={query.sort.order}
            onValueChange={(order) =>
              onQueryChange({
                ...query,
                sort: {
                  ...query.sort,
                  order: order as "asc" | "desc",
                },
              })
            }
            disabled={readOnly}
          >
            <SelectTrigger>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="desc">Descending</SelectItem>
              <SelectItem value="asc">Ascending</SelectItem>
            </SelectContent>
          </Select>
        </div>

        <div className="space-y-2">
          <Label>Manual Pins</Label>
          <Select
            value={orderingMode}
            onValueChange={(mode) =>
              onSortConfigChange({
                ...sortConfig,
                mode,
              })
            }
            disabled={readOnly}
          >
            <SelectTrigger>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="query_sort">Follow Query Sort</SelectItem>
              <SelectItem value="manual_pins">Preserve Manual Pins</SelectItem>
            </SelectContent>
          </Select>
        </div>
      </div>

      <p className="text-muted-foreground text-xs">
        Smart collections always preview using the query sort. Manual pins are stored for the saved
        collection so curated items can stay near the top later.
      </p>
    </div>
  );
}
