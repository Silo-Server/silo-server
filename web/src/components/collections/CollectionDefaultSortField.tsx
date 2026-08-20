import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { collectionDefaultSortOptions } from "@/lib/collectionSortConfig";

interface Props {
  value: string;
  onChange: (next: string) => void;
  /** Personal collections may sort by per-profile fields; library ones may not. */
  allowPersonalized?: boolean;
  inputId?: string;
  disabled?: boolean;
}

/**
 * CollectionDefaultSortField picks the order viewers land on when they open a
 * collection. It is the exact-collection counterpart to CollectionOrderingEditor
 * (which owns ordering for smart collections through their query definition),
 * so both kinds of collection expose a default order without duplicating the
 * ordering-mode controls that only make sense for a query.
 */
export function CollectionDefaultSortField({
  value,
  onChange,
  allowPersonalized = false,
  inputId = "collection-default-sort",
  disabled = false,
}: Props) {
  const options = collectionDefaultSortOptions(allowPersonalized);

  return (
    <div className="space-y-2">
      <Label htmlFor={inputId}>Default Sort</Label>
      <Select value={value} onValueChange={onChange} disabled={disabled}>
        <SelectTrigger id={inputId}>
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {options.map((option) => (
            <SelectItem key={option.value} value={option.value}>
              {option.label}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      <p className="text-muted-foreground text-xs">
        The order viewers get when they open this collection. They can still sort it their own way,
        and that choice is remembered for them.
      </p>
    </div>
  );
}
