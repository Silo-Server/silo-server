import { House, Library as LibraryIcon } from "lucide-react";

import { rowMeta, type CollectionRow } from "@/lib/collections/rows";

/** A Home rows row in a list: a house for Home or a library for a library page, its title and place. */
export function CollectionRowSummary({
  row,
  libraryNames,
}: {
  row: CollectionRow;
  libraryNames: ReadonlyMap<number, string>;
}) {
  const Icon = row.page.kind === "home" ? House : LibraryIcon;
  return (
    <>
      <span className="bg-muted text-muted-foreground grid size-8 shrink-0 place-items-center rounded-lg">
        <Icon aria-hidden className="size-4" />
      </span>
      <span className="grid min-w-0 flex-1">
        <span className="truncate text-[13.5px] font-medium">{row.title}</span>
        <span className="text-muted-foreground truncate text-[12.5px]">
          {rowMeta(row, libraryNames)}
        </span>
      </span>
    </>
  );
}
