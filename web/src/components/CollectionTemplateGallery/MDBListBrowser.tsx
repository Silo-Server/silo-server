import { useId, useMemo, useState } from "react";
import { Info, Link2, Search } from "lucide-react";
import { Link } from "react-router";
import { RadioGroup as RadioGroupPrimitive } from "radix-ui";

import type { MDBListListSummary } from "@/api/types";
import { Input } from "@/components/ui/input";
import { useDebounce } from "@/hooks/useDebounce";
import { useMDBListSearch } from "@/hooks/queries/userCollectionImports";
import type { CollectionTemplate, CollectionTemplateGroup } from "@/lib/collectionTemplates";
import {
  FROM_MDBLIST,
  FROM_MDBLIST_HELP,
  MDBLIST_LINK_INVALID,
  MDBLIST_SEARCH_OFF_PROFILE,
  PASTE_MDBLIST_LINK,
  POPULAR_PICKS,
  POPULAR_PICKS_HELP,
} from "@/lib/collections/copy";
import {
  cleanMDBListLink,
  isMDBListLink,
  mdblistPick,
  mdblistPickId,
  templatePick,
  type SyncedPick,
} from "@/lib/collections/synced";
import type { ScopeKind } from "@/lib/collections/scope";
import { cn } from "@/lib/utils";

import { CollectionTemplateCard, PickRow } from "./CollectionTemplateCard";

const ALL = "all";

function matches(template: CollectionTemplate, term: string) {
  return [template.title, template.description, ...(template.tags ?? [])].some((text) =>
    text.toLowerCase().includes(term),
  );
}

function listMeta(list: MDBListListSummary) {
  const kind = list.mediatype === "movie" ? "Movies" : list.mediatype === "show" ? "TV" : null;
  return [
    `by ${list.user_name}`,
    kind,
    `${list.items.toLocaleString()} title${list.items === 1 ? "" : "s"}`,
    list.likes > 0 ? `♥ ${list.likes.toLocaleString()}` : null,
  ]
    .filter(Boolean)
    .join(" · ");
}

function GroupHeading({ title, help }: { title: string; help: string }) {
  return (
    <p className="px-3.5 pt-3 pb-1 text-[13px]">
      <span className="font-semibold">{title}</span>{" "}
      <span className="text-muted-foreground">{help}</span>
    </p>
  );
}

/**
 * The MDBList tab of the Synced list step. One search box covers the popular
 * picks (filtered here) and MDBList's public lists (searched on the server),
 * theme chips narrow the picks, and any MDBList link can be pasted instead.
 * With MDBList search off, the box is disabled and says why; picks and links
 * still work. A failed search keeps the typed text and the last results.
 */
export function MDBListBrowser({
  scopeKind,
  picks,
  searchEnabled,
  selectedId,
  onPick,
  link,
  onLinkChange,
}: {
  scopeKind: ScopeKind;
  /** Creatable templates that name their list, by category. */
  picks: readonly CollectionTemplateGroup[];
  /** `mdblist_search` from the scope's capabilities. */
  searchEnabled: boolean;
  /** The pick the draft follows, if it came from this tab. */
  selectedId?: string;
  onPick: (pick: SyncedPick) => void;
  /** The pasted link, as typed. */
  link: string;
  onLinkChange: (raw: string) => void;
}) {
  const id = useId();
  const [query, setQuery] = useState("");
  const [theme, setTheme] = useState(ALL);
  // 300ms keeps typing responsive while bounding hits against MDBList's daily quota.
  const settled = useDebounce(query.trim(), 300);
  const search = useMDBListSearch(settled, searchEnabled);
  const [lastFound, setLastFound] = useState<{ query: string; lists: MDBListListSummary[] }>();
  if (search.data && search.data.lists !== lastFound?.lists && settled) {
    setLastFound({ query: settled, lists: search.data.lists });
  }
  // A failed search keeps the last results; a new term shows "Searching…" until it answers.
  const fallback = search.isError ? lastFound?.lists : undefined;
  const found = settled ? (search.data?.lists ?? fallback ?? []) : [];

  const term = query.trim().toLowerCase();
  const shown = useMemo(
    () =>
      picks
        .filter((group) => theme === ALL || group.category === theme)
        .flatMap((group) => group.templates)
        .filter((template) => !term || matches(template, term)),
    [picks, term, theme],
  );
  const templates = useMemo(() => picks.flatMap((group) => group.templates), [picks]);

  function choose(value: string) {
    const template = templates.find((entry) => entry.id === value);
    if (template) return onPick(templatePick(template, scopeKind));
    const list = found.find((entry) => mdblistPickId(entry) === value);
    if (list) onPick(mdblistPick(list));
  }

  const cleaned = cleanMDBListLink(link);
  const linkInvalid = cleaned !== "" && !isMDBListLink(cleaned);

  return (
    <div className="grid gap-3">
      <div className="relative">
        <Search
          aria-hidden
          className="text-muted-foreground absolute top-1/2 left-3 size-4 -translate-y-1/2"
        />
        <Input
          type="search"
          aria-label="Search lists"
          aria-describedby={searchEnabled ? undefined : `${id}-off`}
          placeholder={searchEnabled ? "Search MDBList and popular picks" : "Search is off"}
          disabled={!searchEnabled}
          className="h-11 pl-9"
          value={query}
          onChange={(event) => setQuery(event.target.value)}
        />
      </div>
      {searchEnabled ? null : (
        <p
          id={`${id}-off`}
          className="bg-muted/60 flex items-start gap-2.5 rounded-xl px-3 py-2.5 text-[13px]"
        >
          <Info aria-hidden className="text-muted-foreground mt-0.5 size-4 shrink-0" />
          {scopeKind === "server" ? (
            <span>
              Searching MDBList needs an API key in{" "}
              <Link
                to="/admin/settings/providers"
                className="font-medium underline underline-offset-4"
              >
                Settings › Subtitles &amp; Metadata
              </Link>
              . Popular picks and pasted links still work.
            </span>
          ) : (
            <span>{MDBLIST_SEARCH_OFF_PROFILE}</span>
          )}
        </p>
      )}
      {picks.length > 1 ? (
        <div role="group" aria-label="Themes" className="flex flex-wrap gap-2">
          {[{ category: ALL, label: "All" }, ...picks].map((group) => (
            <button
              key={group.category}
              type="button"
              aria-pressed={theme === group.category}
              onClick={() => setTheme(group.category)}
              className={cn(
                "border-border hover:bg-accent h-8 rounded-full border px-3 text-[13px] font-medium",
                theme === group.category && "bg-foreground text-background hover:bg-foreground",
              )}
            >
              {group.label}
            </button>
          ))}
        </div>
      ) : null}

      <RadioGroupPrimitive.Root
        aria-label="Lists"
        value={selectedId ?? ""}
        onValueChange={choose}
        className="border-border divide-border/60 grid max-h-[420px] overflow-y-auto rounded-xl border"
      >
        {shown.length > 0 ? (
          <div>
            <GroupHeading title={POPULAR_PICKS} help={POPULAR_PICKS_HELP} />
            {shown.map((template) => (
              <CollectionTemplateCard key={template.id} template={template} />
            ))}
          </div>
        ) : null}
        {settled ? (
          <div>
            <GroupHeading title={FROM_MDBLIST} help={FROM_MDBLIST_HELP} />
            {found.map((list) => (
              <PickRow
                key={list.id}
                value={mdblistPickId(list)}
                title={list.name}
                meta={listMeta(list)}
              />
            ))}
            {search.isError ? (
              <p role="alert" className="text-destructive px-3.5 py-2.5 text-[13px]">
                {`Couldn't search MDBList. ${search.error instanceof Error ? search.error.message : ""}`.trim()}
              </p>
            ) : search.isFetching && found.length === 0 ? (
              <p className="text-muted-foreground px-3.5 py-2.5 text-[13px]">Searching…</p>
            ) : search.data && found.length === 0 ? (
              <p className="text-muted-foreground px-3.5 py-2.5 text-[13px]">
                No public lists match “{settled}”.
              </p>
            ) : null}
          </div>
        ) : null}
        {shown.length === 0 && !settled ? (
          <p className="text-muted-foreground px-3.5 py-3 text-[13px]">
            No popular picks here. Search MDBList or paste a link.
          </p>
        ) : null}
      </RadioGroupPrimitive.Root>

      <div className="grid gap-2">
        <label htmlFor={`${id}-link`} className="text-[14px] font-semibold">
          {PASTE_MDBLIST_LINK}
        </label>
        <div className="relative">
          <Link2
            aria-hidden
            className="text-muted-foreground absolute top-1/2 left-3 size-4 -translate-y-1/2"
          />
          <Input
            id={`${id}-link`}
            type="url"
            inputMode="url"
            placeholder="https://mdblist.com/lists/…"
            className="h-11 pl-9"
            aria-invalid={linkInvalid || undefined}
            aria-describedby={linkInvalid ? `${id}-link-error` : undefined}
            value={link}
            onChange={(event) => onLinkChange(event.target.value)}
          />
        </div>
        {linkInvalid ? (
          <p id={`${id}-link-error`} className="text-destructive text-[12.5px]">
            {MDBLIST_LINK_INVALID}
          </p>
        ) : null}
      </div>
    </div>
  );
}
