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
  fromMdblist,
  fromMdblistHelp,
  mdblistLinkInvalid,
  mdblistSearchOffProfile,
  pasteMdblistLink,
  popularPicks,
  popularPicksHelp,
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
import { formatLocale } from "@/lib/appLocale";
import { useT, type Translate } from "@/i18n";

const ALL = "all";

/** A link, not text: the same in every language. */
const MDBLIST_LINK_PLACEHOLDER = "https://mdblist.com/lists/…";

function matches(template: CollectionTemplate, term: string) {
  return [template.title, template.description, ...(template.tags ?? [])].some((text) =>
    text.toLowerCase().includes(term),
  );
}

function listMeta(t: Translate, list: MDBListListSummary) {
  const kind =
    list.mediatype === "movie" || list.mediatype === "show"
      ? t("web.collections.templateGallery.listKind", { kind: list.mediatype })
      : null;
  return [
    t("web.collections.templateGallery.byUser", { user: list.user_name }),
    kind,
    t("web.collections.metaLine.titleCount", {
      count: list.items,
      shown: list.items.toLocaleString(formatLocale()),
    }),
    list.likes > 0 ? `♥ ${list.likes.toLocaleString(formatLocale())}` : null,
  ]
    .filter(Boolean)
    .join(" · ");
}

function GroupHeading({ title, help }: { title: string; help: string }) {
  const t = useT();
  return (
    <p className="px-3.5 pt-3 pb-1 text-[13px]">
      {t("web.collections.templateGallery.groupHeading", {
        title,
        help,
        b: (chunks) => <span className="font-semibold">{chunks}</span>,
        muted: (chunks) => <span className="text-muted-foreground">{chunks}</span>,
      })}
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
  const t = useT();
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
          aria-label={t("web.collections.templateGallery.searchLists")}
          aria-describedby={searchEnabled ? undefined : `${id}-off`}
          placeholder={
            searchEnabled
              ? t("web.collections.templateGallery.searchPlaceholder")
              : t("web.collections.templateGallery.searchOff")
          }
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
              {t("web.collections.templateGallery.searchNeedsKey", {
                link: (chunks) => (
                  <Link
                    to="/admin/settings/providers"
                    className="font-medium underline underline-offset-4"
                  >
                    {chunks}
                  </Link>
                ),
              })}
            </span>
          ) : (
            <span>{mdblistSearchOffProfile()}</span>
          )}
        </p>
      )}
      {picks.length > 1 ? (
        <div
          role="group"
          aria-label={t("web.collections.templateGallery.themes")}
          className="flex flex-wrap gap-2"
        >
          {[{ category: ALL, label: t("web.collections.templateGallery.allThemes") }, ...picks].map(
            (group) => (
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
            ),
          )}
        </div>
      ) : null}

      <RadioGroupPrimitive.Root
        aria-label={t("web.collections.templateGallery.lists")}
        value={selectedId ?? ""}
        onValueChange={choose}
        className="border-border divide-border/60 grid max-h-[420px] overflow-y-auto rounded-xl border"
      >
        {shown.length > 0 ? (
          <div>
            <GroupHeading title={popularPicks()} help={popularPicksHelp()} />
            {shown.map((template) => (
              <CollectionTemplateCard key={template.id} template={template} />
            ))}
          </div>
        ) : null}
        {settled ? (
          <div>
            <GroupHeading title={fromMdblist()} help={fromMdblistHelp()} />
            {found.map((list) => (
              <PickRow
                key={list.id}
                value={mdblistPickId(list)}
                title={list.name}
                meta={listMeta(t, list)}
              />
            ))}
            {search.isError ? (
              <p role="alert" className="text-destructive px-3.5 py-2.5 text-[13px]">
                {t("web.collections.templateGallery.searchFailed", {
                  detail: search.error instanceof Error ? search.error.message : "",
                }).trim()}
              </p>
            ) : search.isFetching && found.length === 0 ? (
              <p className="text-muted-foreground px-3.5 py-2.5 text-[13px]">
                {t("web.collections.templateGallery.searching")}
              </p>
            ) : search.data && found.length === 0 ? (
              <p className="text-muted-foreground px-3.5 py-2.5 text-[13px]">
                {t("web.collections.templateGallery.noMatches", { query: settled })}
              </p>
            ) : null}
          </div>
        ) : null}
        {shown.length === 0 && !settled ? (
          <p className="text-muted-foreground px-3.5 py-3 text-[13px]">
            {t("web.collections.templateGallery.noPicks")}
          </p>
        ) : null}
      </RadioGroupPrimitive.Root>

      <div className="grid gap-2">
        <label htmlFor={`${id}-link`} className="text-[14px] font-semibold">
          {pasteMdblistLink()}
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
            placeholder={MDBLIST_LINK_PLACEHOLDER}
            className="h-11 pl-9"
            aria-invalid={linkInvalid || undefined}
            aria-describedby={linkInvalid ? `${id}-link-error` : undefined}
            value={link}
            onChange={(event) => onLinkChange(event.target.value)}
          />
        </div>
        {linkInvalid ? (
          <p id={`${id}-link-error`} className="text-destructive text-[12.5px]">
            {mdblistLinkInvalid()}
          </p>
        ) : null}
      </div>
    </div>
  );
}
