import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuCheckboxItem,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { ChevronDown } from "lucide-react";

interface LibraryOption {
  id: number;
  name: string;
  type?: string;
}

function isLibraryEligible(library: LibraryOption, eligibleKinds?: string[]): boolean {
  if (!eligibleKinds || eligibleKinds.length === 0) return true;
  if (!library.type) return true;
  if (library.type === "mixed") return true;
  return eligibleKinds.includes(library.type);
}

function formatLibraryFilterSummary(
  libraryIds: number[],
  libraries: LibraryOption[],
  emptyLabel: string,
): string {
  if (libraryIds.length === 0) {
    return emptyLabel;
  }

  const names = libraryIds
    .map((libraryId) => libraries.find((library) => library.id === libraryId)?.name)
    .filter((name): name is string => Boolean(name));

  if (names.length === 0) {
    return `${libraryIds.length} libraries`;
  }
  if (names.length === 1) {
    return names[0] ?? "1 library";
  }
  if (names.length === 2) {
    return `${names[0] ?? "Library"}, ${names[1] ?? "Library"}`;
  }
  return `${names[0] ?? "Library"} +${names.length - 1} more`;
}

function toggleLibrarySelection(
  selectedIds: number[],
  libraryId: number,
  checked: boolean,
): number[] {
  if (checked) {
    return selectedIds.includes(libraryId) ? selectedIds : [...selectedIds, libraryId];
  }
  return selectedIds.filter((id) => id !== libraryId);
}

export default function LibraryMultiSelect({
  libraries,
  value,
  onChange,
  eligibleKinds,
  emptyLabel = "All Libraries",
  hideAllOption = false,
  ineligibleReason,
  triggerClassName,
  disabled,
}: {
  libraries: LibraryOption[];
  value: number[];
  onChange: (libraryIds: number[]) => void;
  eligibleKinds?: string[];
  emptyLabel?: string;
  hideAllOption?: boolean;
  ineligibleReason?: string;
  triggerClassName?: string;
  disabled?: boolean;
}) {
  const hasIneligible =
    Array.isArray(eligibleKinds) &&
    eligibleKinds.length > 0 &&
    libraries.some((library) => !isLibraryEligible(library, eligibleKinds));

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button
          type="button"
          variant="outline"
          className={triggerClassName ?? "w-full justify-between"}
          disabled={disabled}
        >
          <span className="truncate">
            {formatLibraryFilterSummary(value, libraries, emptyLabel)}
          </span>
          <ChevronDown className="ml-2 h-4 w-4 shrink-0" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent className="w-[260px]" align="start">
        {hideAllOption ? null : (
          <>
            <DropdownMenuItem
              disabled={value.length === 0}
              onSelect={(event) => {
                event.preventDefault();
                onChange([]);
              }}
            >
              {emptyLabel}
            </DropdownMenuItem>
            <DropdownMenuSeparator />
          </>
        )}
        {libraries.map((library) => {
          const eligible = isLibraryEligible(library, eligibleKinds);
          const selected = value.includes(library.id);
          // Allow deselecting an already-selected ineligible library (e.g.
          // eligibility shrank after the library type changed), but block
          // selecting a new ineligible one.
          const handleCheckedChange = (checked: boolean | "indeterminate") => {
            const next = Boolean(checked);
            if (!eligible && next) return;
            onChange(toggleLibrarySelection(value, library.id, next));
          };
          return (
            <DropdownMenuCheckboxItem
              key={library.id}
              checked={selected}
              disabled={!eligible && !selected}
              onCheckedChange={handleCheckedChange}
              onSelect={(event) => event.preventDefault()}
            >
              {library.name}
            </DropdownMenuCheckboxItem>
          );
        })}
        {hasIneligible && ineligibleReason ? (
          <>
            <DropdownMenuSeparator />
            <DropdownMenuLabel className="text-muted-foreground text-xs leading-snug font-normal whitespace-normal">
              {ineligibleReason}
            </DropdownMenuLabel>
          </>
        ) : null}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
