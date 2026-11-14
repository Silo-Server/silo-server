import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { useT } from "@/i18n";
import { getDefaultQuerySortOrder, getQuerySortOptions } from "@/lib/querySortOptions";

interface Filters {
  type: string;
  sort: string;
  order: string;
}

interface FilterBarProps {
  filters: Filters;
  onChange: (filters: Filters) => void;
}

export default function FilterBar({ filters, onChange }: FilterBarProps) {
  const t = useT();
  const sortOptions = getQuerySortOptions(false);

  return (
    <div className="flex flex-wrap gap-3">
      <Select value={filters.type} onValueChange={(v) => onChange({ ...filters, type: v })}>
        <SelectTrigger className="w-32">
          <SelectValue placeholder={t("web.catalog.filters.typePlaceholder")} />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value="all">{t("web.catalog.mediaType.all")}</SelectItem>
          <SelectItem value="movie">{t("web.catalog.mediaType.movies")}</SelectItem>
          <SelectItem value="series">{t("web.catalog.mediaType.series")}</SelectItem>
        </SelectContent>
      </Select>

      <Select
        value={filters.sort}
        onValueChange={(v) => onChange({ ...filters, sort: v, order: getDefaultQuerySortOrder(v) })}
      >
        <SelectTrigger className="w-40">
          <SelectValue placeholder={t("web.catalog.sort.sortBy")} />
        </SelectTrigger>
        <SelectContent>
          {sortOptions.map((option) => (
            <SelectItem key={option.value} value={option.value}>
              {option.label}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>

      <Select value={filters.order} onValueChange={(v) => onChange({ ...filters, order: v })}>
        <SelectTrigger className="w-28">
          <SelectValue placeholder={t("web.catalog.sort.order")} />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value="desc">{t("web.catalog.sort.descShort")}</SelectItem>
          <SelectItem value="asc">{t("web.catalog.sort.ascShort")}</SelectItem>
        </SelectContent>
      </Select>
    </div>
  );
}
