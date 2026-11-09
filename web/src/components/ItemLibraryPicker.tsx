import { useMemo } from "react";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { useAdminLibraries } from "@/hooks/queries/admin/libraries";
import { useT } from "@/i18n";

interface ItemLibraryPickerProps {
  /** The item's own libraries; the picker offers nothing else. */
  libraryIds: number[];
  value: number | undefined;
  onChange: (libraryId: number) => void;
}

/**
 * Asks which library a match search runs in when the item is in several and
 * the dialog cannot tell which one is meant. The server refuses to guess, and
 * the library decides which metadata settings the search uses.
 */
export function ItemLibraryPicker({ libraryIds, value, onChange }: ItemLibraryPickerProps) {
  const t = useT();
  const { data: libraries } = useAdminLibraries();
  const names = useMemo(
    () => new Map((libraries ?? []).map((library) => [library.id, library.name])),
    [libraries],
  );

  return (
    <div className="space-y-1">
      <Label>{t("web.item.libraryPicker.label")}</Label>
      <Select
        value={value === undefined ? "" : String(value)}
        onValueChange={(v) => onChange(Number(v))}
      >
        <SelectTrigger aria-label={t("web.item.libraryPicker.label")}>
          <SelectValue placeholder={t("web.item.libraryPicker.placeholder")} />
        </SelectTrigger>
        <SelectContent>
          {libraryIds.map((id) => (
            <SelectItem key={id} value={String(id)}>
              {names.get(id) ?? t("web.item.libraryPicker.fallbackName", { id: String(id) })}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      <p className="text-muted-foreground text-xs">{t("web.item.libraryPicker.help")}</p>
    </div>
  );
}
