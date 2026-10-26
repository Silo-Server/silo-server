import { useId } from "react";

import type { UserCollectionMediaFilter, UserCollectionWatchFilter } from "@/api/types";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  COLLECTION_MEDIA_FILTER_OPTIONS,
  COLLECTION_WATCH_FILTER_OPTIONS,
  displayFiltersToQueryDefinition,
  queryDefinitionToDisplayFilters,
} from "@/lib/collectionDisplayFilters";
import type { CollectionDraft } from "@/lib/collections/scope";

function ShowOnlyField({
  value,
  onChange,
}: {
  value: CollectionDraft["showOnly"];
  onChange: (next: CollectionDraft["showOnly"]) => void;
}) {
  const id = useId();
  const { watch, media } = queryDefinitionToDisplayFilters(value);
  const commit = (nextWatch: UserCollectionWatchFilter, nextMedia: UserCollectionMediaFilter) =>
    onChange(displayFiltersToQueryDefinition(nextWatch, nextMedia));
  return (
    <fieldset className="grid gap-2" aria-describedby={`${id}-help`}>
      <legend className="mb-2 text-[14.5px] font-semibold">Show only</legend>
      <div className="grid grid-cols-2 gap-2">
        <Select
          value={watch}
          onValueChange={(next) => commit(next as UserCollectionWatchFilter, media)}
        >
          <SelectTrigger aria-label="Watch state" className="w-full">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {COLLECTION_WATCH_FILTER_OPTIONS.map((option) => (
              <SelectItem key={option.value} value={option.value}>
                {option.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Select
          value={media}
          onValueChange={(next) => commit(watch, next as UserCollectionMediaFilter)}
        >
          <SelectTrigger aria-label="Content" className="w-full">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {COLLECTION_MEDIA_FILTER_OPTIONS.map((option) => (
              <SelectItem key={option.value} value={option.value}>
                {option.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
      <p id={`${id}-help`} className="text-muted-foreground text-[12.5px] leading-snug">
        Hides titles while browsing, for example the ones you&apos;ve finished. Nothing is removed.
      </p>
    </fieldset>
  );
}

/** Name and description; and Show only on a personal Manual collection or Synced list. */
export function DetailsPanel({
  draft,
  onChange,
  showOnly,
  nameNote,
}: {
  draft: CollectionDraft;
  onChange: (update: (draft: CollectionDraft) => CollectionDraft) => void;
  /** Personal Manual collections and Synced lists filter what they show while browsing. */
  showOnly: boolean;
  /** A line under Name, as in "Kept your name". */
  nameNote?: string;
}) {
  const id = useId();
  return (
    <section
      aria-label="Name and description"
      className="surface-panel grid content-start gap-4 rounded-[22px] p-5 sm:p-6"
    >
      <div className="grid gap-2">
        <label htmlFor={`${id}-name`} className="text-[14.5px] font-semibold">
          Name
        </label>
        <Input
          id={`${id}-name`}
          required
          aria-describedby={nameNote ? `${id}-name-note` : undefined}
          value={draft.name}
          onChange={(event) => {
            const name = event.target.value;
            onChange((current) => ({ ...current, name }));
          }}
        />
        {nameNote ? (
          <p id={`${id}-name-note`} role="status" className="text-muted-foreground text-[12.5px]">
            {nameNote}
          </p>
        ) : null}
      </div>
      <div className="grid gap-2">
        <label htmlFor={`${id}-description`} className="text-[14.5px] font-semibold">
          Description <span className="text-muted-foreground font-normal">Optional</span>
        </label>
        <textarea
          id={`${id}-description`}
          rows={3}
          className="border-input placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-ring/50 dark:bg-input/30 w-full rounded-md border bg-transparent px-3 py-2 text-base outline-none focus-visible:ring-[3px] md:text-sm"
          value={draft.description}
          onChange={(event) => {
            const description = event.target.value;
            onChange((current) => ({ ...current, description }));
          }}
        />
      </div>
      {showOnly ? (
        <ShowOnlyField
          value={draft.showOnly}
          onChange={(next) => onChange((current) => ({ ...current, showOnly: next }))}
        />
      ) : null}
    </section>
  );
}
