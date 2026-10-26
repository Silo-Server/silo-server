import { useId } from "react";

import { Input } from "@/components/ui/input";
import {
  FIND_FRANCHISE_ID,
  FRANCHISE_ID_HELP,
  FRANCHISE_ID_INVALID,
  FRANCHISE_ID_LABEL,
  FRANCHISE_ID_MISSING,
} from "@/lib/collections/copy";
import { franchiseIdOf } from "@/lib/collections/synced";

/**
 * The TMDB collection a franchise list follows, by the number in its
 * themoviedb.org link. The link searches TMDB for the list's name.
 */
export function FranchiseIdField({
  value,
  name,
  onChange,
}: {
  value: string;
  /** The list's name, to search TMDB with. */
  name: string;
  onChange: (value: string) => void;
}) {
  const id = useId();
  const invalid = value.trim() !== "" && franchiseIdOf(value) === null;
  let note = FRANCHISE_ID_HELP;
  if (invalid) note = FRANCHISE_ID_INVALID;
  else if (value.trim() === "") note = FRANCHISE_ID_MISSING;
  return (
    <div className="grid gap-2">
      <label htmlFor={id} className="text-[14px] font-semibold">
        {FRANCHISE_ID_LABEL}
      </label>
      <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
        <Input
          id={id}
          inputMode="numeric"
          autoComplete="off"
          value={value}
          placeholder="119"
          aria-invalid={invalid || undefined}
          aria-describedby={`${id}-note`}
          onChange={(event) => onChange(event.target.value)}
          className="h-11 w-40"
        />
        <a
          href={`https://www.themoviedb.org/search/collection?query=${encodeURIComponent(name)}`}
          target="_blank"
          rel="noreferrer"
          className="text-[13.5px] font-medium underline underline-offset-4"
        >
          {FIND_FRANCHISE_ID}
        </a>
      </div>
      <p
        id={`${id}-note`}
        className={invalid ? "text-destructive text-[13px]" : "text-muted-foreground text-[13px]"}
      >
        {note}
      </p>
    </div>
  );
}
