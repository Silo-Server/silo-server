import { useId } from "react";
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
 * ("My Home", "My Kids page"). Disabled with its reason in visible text when
 * the collection can't be added yet.
 */
export function AddAsRowMenu({
  bound,
  others,
  mine = false,
  disabledReason,
  onPick,
}: {
  bound: readonly NamedLibrary[];
  others: readonly NamedLibrary[];
  mine?: boolean;
  disabledReason?: string | null;
  onPick: (page: PageRef) => void;
}) {
  const reasonId = useId();
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
    <div className="grid justify-items-start gap-1.5">
      <DropdownMenu>
        <DropdownMenuTrigger asChild disabled={Boolean(disabledReason)}>
          <Button
            variant="outline"
            size="sm"
            aria-describedby={disabledReason ? reasonId : undefined}
          >
            <Plus aria-hidden />
            {ADD_AS_A_ROW}
            <ChevronDown aria-hidden className="opacity-70" />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="start" className="min-w-[220px]">
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
      {disabledReason ? (
        <p id={reasonId} className="text-muted-foreground text-[12.5px]">
          {disabledReason}
        </p>
      ) : null}
    </div>
  );
}
