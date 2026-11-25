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
import { Textarea } from "@/components/ui/textarea";
import {
  COLLECTION_MEDIA_FILTER_OPTIONS,
  COLLECTION_WATCH_FILTER_OPTIONS,
  displayFiltersToQueryDefinition,
  queryDefinitionToDisplayFilters,
} from "@/lib/collectionDisplayFilters";
import type { CollectionDraft } from "@/lib/collections/scope";
import { useT } from "@/i18n";

import { ToggleRow } from "../fields/ToggleRow";

function ShowOnlyField({
  value,
  onChange,
}: {
  value: CollectionDraft["showOnly"];
  onChange: (next: CollectionDraft["showOnly"]) => void;
}) {
  const t = useT();
  const id = useId();
  const { watch, media } = queryDefinitionToDisplayFilters(value);
  const commit = (nextWatch: UserCollectionWatchFilter, nextMedia: UserCollectionMediaFilter) =>
    onChange(displayFiltersToQueryDefinition(nextWatch, nextMedia));
  return (
    <fieldset className="grid gap-2" aria-describedby={`${id}-help`}>
      <legend className="mb-2 text-[0.90625rem] font-semibold">
        {t("web.collections.copy.fieldShowOnly")}
      </legend>
      <div className="grid grid-cols-2 gap-2">
        <Select
          value={watch}
          onValueChange={(next) => commit(next as UserCollectionWatchFilter, media)}
        >
          <SelectTrigger aria-label={t("web.collections.details.watchState")} className="w-full">
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
          <SelectTrigger aria-label={t("web.collections.details.content")} className="w-full">
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
      <p id={`${id}-help`} className="text-muted-foreground text-[0.78125rem] leading-snug">
        {t("web.collections.details.showOnlyHelp")}
      </p>
    </fieldset>
  );
}

/**
 * Name and description; Show only on a personal Manual collection or Synced
 * list; and Hide watched titles on a server collection.
 */
export function DetailsPanel({
  draft,
  onChange,
  showOnly,
  hideWatched = false,
  nameNote,
  nameError,
}: {
  draft: CollectionDraft;
  onChange: (update: (draft: CollectionDraft) => CollectionDraft) => void;
  /** Personal Manual collections and Synced lists filter what they show while browsing. */
  showOnly: boolean;
  /** Server collections can hide, from each profile, the titles it has watched. */
  hideWatched?: boolean;
  /** A line under Name, as in "Kept your name". */
  nameNote?: string;
  /** Why the last save refused this name, as in "Another collection in Movies already has this name". */
  nameError?: string | null;
}) {
  const t = useT();
  const id = useId();
  const nameDescribedBy =
    [nameError ? `${id}-name-error` : null, nameNote ? `${id}-name-note` : null]
      .filter(Boolean)
      .join(" ") || undefined;
  return (
    <section
      aria-label={t("web.collections.details.label")}
      className="surface-panel grid content-start gap-4 rounded-[22px] p-5 sm:p-6"
    >
      <div className="grid gap-2">
        <label htmlFor={`${id}-name`} className="text-[0.90625rem] font-semibold">
          {t("web.collections.copy.fieldName")}
        </label>
        <Input
          id={`${id}-name`}
          required
          aria-invalid={nameError ? true : undefined}
          aria-describedby={nameDescribedBy}
          value={draft.name}
          onChange={(event) => {
            const name = event.target.value;
            onChange((current) => ({ ...current, name }));
          }}
        />
        {nameError ? (
          <p id={`${id}-name-error`} role="alert" className="text-destructive text-[0.78125rem]">
            {nameError}
          </p>
        ) : null}
        {nameNote ? (
          <p
            id={`${id}-name-note`}
            role="status"
            className="text-muted-foreground text-[0.78125rem]"
          >
            {nameNote}
          </p>
        ) : null}
      </div>
      <div className="grid gap-2">
        <label htmlFor={`${id}-description`} className="text-[0.90625rem] font-semibold">
          {t("web.collections.details.descriptionLabel", {
            field: t("web.collections.copy.fieldDescription"),
            muted: (chunks) => <span className="text-muted-foreground font-normal">{chunks}</span>,
          })}
        </label>
        <Textarea
          id={`${id}-description`}
          rows={3}
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
      {hideWatched && draft.server ? (
        <ToggleRow
          label={t("web.collections.details.hideWatched")}
          help={t("web.collections.details.hideWatchedHelp")}
          checked={draft.server.hideWatched ?? false}
          onCheckedChange={(on) =>
            onChange((current) =>
              current.server
                ? { ...current, server: { ...current.server, hideWatched: on } }
                : current,
            )
          }
        />
      ) : null}
    </section>
  );
}
