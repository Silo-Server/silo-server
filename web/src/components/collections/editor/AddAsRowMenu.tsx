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
import { ADD_AS_A_ROW, OTHER_LIBRARIES, libraryPageLabel } from "@/lib/collections/copy";
import type { PageRef } from "@/lib/homeRows/types";

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
  const own = (place: string) => (mine ? `My ${place}` : place);
  const libraryItem = (library: NamedLibrary) => (
    <DropdownMenuItem
      key={library.id}
      onSelect={() => onPick({ kind: "library", libraryId: library.id })}
    >
      <LibraryIcon aria-hidden />
      {own(libraryPageLabel(library.name))}
    </DropdownMenuItem>
  );
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild disabled={disabled}>
        <Button variant="outline" size="sm" aria-describedby={describedBy}>
          <Plus aria-hidden />
          {ADD_AS_A_ROW}
          <ChevronDown aria-hidden className="opacity-70" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="min-w-[220px]">
        <DropdownMenuItem onSelect={() => onPick({ kind: "home" })}>
          <House aria-hidden />
          {own("Home")}
        </DropdownMenuItem>
        {bound.length > 0 ? <DropdownMenuSeparator /> : null}
        {bound.map(libraryItem)}
        {others.length > 0 ? (
          <>
            <DropdownMenuSeparator />
            <DropdownMenuLabel className="text-muted-foreground text-[12px] font-medium">
              {OTHER_LIBRARIES}
            </DropdownMenuLabel>
            {others.map(libraryItem)}
          </>
        ) : null}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
