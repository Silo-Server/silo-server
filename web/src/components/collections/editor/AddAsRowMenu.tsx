import { ChevronDown, House, Library as LibraryIcon, Plus } from "lucide-react";

import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { addAsARow, otherLibraries, libraryPageLabel } from "@/lib/collections/copy";
import type { PageRef } from "@/lib/homeRows/types";
import { useT } from "@/i18n";

interface NamedLibrary {
  id: number;
  name: string;
}

/**
 * "Add as a row ▾": Home, then the library pages of the collection's own
 * libraries, then the other libraries. `mine` names the viewer's own pages
 * ("My Home", "My Kids page"). While the collection can't be added yet it is
 * disabled, and `describedBy` points at the line that says why.
 */
export function AddAsRowMenu({
  bound,
  others,
  mine = false,
  disabled = false,
  describedBy,
  onPick,
}: {
  bound: readonly NamedLibrary[];
  others: readonly NamedLibrary[];
  mine?: boolean;
  disabled?: boolean;
  describedBy?: string;
  onPick: (page: PageRef) => void;
}) {
  const t = useT();
  const libraryItem = (library: NamedLibrary) => (
    <DropdownMenuItem
      key={library.id}
      onSelect={() => onPick({ kind: "library", libraryId: library.id })}
    >
      <LibraryIcon aria-hidden />
      {mine
        ? t("web.collections.rows.myLibraryPage", { library: library.name })
        : libraryPageLabel(library.name)}
    </DropdownMenuItem>
  );
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild disabled={disabled}>
        <Button variant="outline" size="sm" aria-describedby={describedBy}>
          <Plus aria-hidden />
          {addAsARow()}
          <ChevronDown aria-hidden className="opacity-70" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="min-w-[220px]">
        <DropdownMenuItem onSelect={() => onPick({ kind: "home" })}>
          <House aria-hidden />
          {mine ? t("web.collections.rows.myHome") : t("web.collections.rows.home")}
        </DropdownMenuItem>
        {bound.length > 0 ? <DropdownMenuSeparator /> : null}
        {bound.map(libraryItem)}
        {others.length > 0 ? (
          <>
            <DropdownMenuSeparator />
            <DropdownMenuLabel className="text-muted-foreground text-[12px] font-medium">
              {otherLibraries()}
            </DropdownMenuLabel>
            {others.map(libraryItem)}
          </>
        ) : null}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
