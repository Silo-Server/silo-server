import type { Library } from "@/api/types";
import { Badge } from "@/components/ui/badge";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";

interface LibraryAccessSelectorProps {
  libraries: Library[];
  value: number[] | null;
  onChange: (value: number[] | null) => void;
  // Label for the null state. Groups and invitations use the default "All
  // libraries"; the per-user policy form passes "Inherit from group", where
  // null means the group's library scope applies.
  allLabel?: string;
  // Optional helper line shown while value is null (e.g. what the inherited
  // scope currently resolves to).
  emptyHint?: string;
}

function sortByLibraryOrder(libraries: Library[], ids: number[]) {
  const selected = new Set(ids);
  return libraries.filter((library) => selected.has(library.id)).map((library) => library.id);
}

export function LibraryAccessSelector({
  libraries,
  value,
  onChange,
  allLabel = "All libraries",
  emptyHint,
}: LibraryAccessSelectorProps) {
  const allLibraries = value === null;

  function handleAllLibrariesChange(checked: boolean) {
    onChange(checked ? null : libraries.map((library) => library.id));
  }

  function handleLibraryToggle(libraryId: number, checked: boolean) {
    const current = value ?? libraries.map((library) => library.id);
    const next = checked
      ? sortByLibraryOrder(libraries, [...current, libraryId])
      : current.filter((id) => id !== libraryId);
    onChange(next);
  }

  return (
    <div className="space-y-2">
      <div className="flex items-center justify-between">
        <Label>Library Access</Label>
        <div className="flex items-center gap-2">
          <span className="text-muted-foreground text-xs">{allLabel}</span>
          <Switch checked={allLibraries} onCheckedChange={handleAllLibrariesChange} />
        </div>
      </div>

      {allLibraries && emptyHint && <p className="text-muted-foreground text-xs">{emptyHint}</p>}

      {!allLibraries && (
        <div className="grid gap-1.5">
          {libraries.length === 0 ? (
            <p className="text-muted-foreground text-xs">No libraries available.</p>
          ) : (
            libraries.map((library) => {
              const checked = value?.includes(library.id) ?? false;
              return (
                <div
                  key={library.id}
                  className="border-border flex items-center justify-between gap-3 rounded-md border px-3 py-1.5"
                >
                  <div className="flex min-w-0 items-center gap-2">
                    <span className="text-sm">{library.name}</span>
                    {!library.enabled && (
                      <Badge variant="outline" className="px-1 py-0 text-[10px]">
                        Disabled
                      </Badge>
                    )}
                    <span className="text-muted-foreground text-xs capitalize">{library.type}</span>
                  </div>
                  <Switch
                    checked={checked}
                    onCheckedChange={(nextChecked) => handleLibraryToggle(library.id, nextChecked)}
                  />
                </div>
              );
            })
          )}
        </div>
      )}
    </div>
  );
}
