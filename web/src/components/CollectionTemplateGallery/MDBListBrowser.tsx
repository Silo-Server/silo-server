import { useId, useMemo, useState } from "react";
import { Info, Link2, Lock, Search } from "lucide-react";
import { Link } from "react-router";
import { RadioGroup as RadioGroupPrimitive } from "radix-ui";

import type { MDBListListSummary } from "@/api/types";
import { LineName, MetaLine } from "@/components/MetaLine";
import { Input } from "@/components/ui/input";
import { useDebounce } from "@/hooks/useDebounce";
import {
  useMDBListAccountLists,
  useMDBListSearch,
  type MDBListAccountList,
} from "@/hooks/queries/userCollectionImports";
import type { CollectionTemplate, CollectionTemplateGroup } from "@/lib/collectionTemplates";
import {
  fromMdblist,
  fromMdblistHelp,
  mdblistLinkInvalid,
  mdblistSearchOffProfile,
  pasteMdblistLink,
  popularPicks,
  popularPicksHelp,
  yourMdblistLists,
  yourMdblistListsServerHelp,
} from "@/lib/collections/copy";
import {
  cleanMDBListLink,
  isMDBListLink,
  isMdblistAccountPickId,
  mdblistAccountPick,
  mdblistAccountPickId,
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

/** What the list box shows: the key account's own lists, or popular picks and MDBList search. */
type View = "own" | "browse";

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
  const user = list.user_name.trim();
  return (
    <MetaLine
      parts={[
        user
          ? t("web.collections.templateGallery.byUser", { user: <LineName name={user} /> })
          : null,
        kind,
        t("web.collections.metaLine.titleCount", {
          count: list.items,
          shown: list.items.toLocaleString(formatLocale()),
        }),
        list.likes > 0 ? `♥ ${list.likes.toLocaleString(formatLocale())}` : null,
      ]}
    />
  );
}

function accountListMeta(t: Translate, list: MDBListAccountList) {
  const [only] = list.media_types;
  return (
    <MetaLine
      parts={[
        list.owned ? null : t("web.collections.templateGallery.listLiked"),
        list.media_types.length === 1 && only
          ? t("web.collections.templateGallery.listKind", { kind: only })
          : null,
        t("web.collections.metaLine.titleCount", {
          count: list.items,
          shown: list.items.toLocaleString(formatLocale()),
        }),
      ]}
    />
  );
}

function GroupHeading({ title, help }: { title: string; help: string }) {
  const t = useT();
  return (
    <p className="px-3.5 pt-3 pb-1 text-[0.8125rem]">
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
 * The MDBList tab of the Synced list step. When the scope's key can list its
 * account's own lists, private ones included, a switch shows them first ("Your
 * lists") and the search box filters them by name; "Browse MDBList" shows the
 * popular picks (filtered here, narrowed by theme chips) and MDBList's public
 * lists (searched on the server). Any MDBList link can be pasted instead, and
 * the field says when a private one works. With MDBList search off, browsing
 * disables the box and says why; picks and links still work. A failed search
 * keeps the typed text and the last results.
 */
export function MDBListBrowser({
  scopeKind,
  picks,
  searchEnabled,
  accountListsEnabled = false,
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
  /** `mdblist_account_lists` from the scope's capabilities. */
  accountListsEnabled?: boolean;
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
  // A pick made while browsing reopens there; anything else starts on the account's own lists.
  const [view, setView] = useState<View>(() =>
    selectedId && !isMdblistAccountPickId(selectedId) ? "browse" : "own",
  );
  const accountLists = useMDBListAccountLists(scopeKind, accountListsEnabled);
  // An account with no lists has nothing to switch to.
  const ownOffered =
    accountListsEnabled && !(accountLists.isSuccess && accountLists.data.length === 0);
  const showing: View = ownOffered ? view : "browse";
  const browsing = showing === "browse";
  // 300ms keeps typing responsive while bounding hits against MDBList's daily quota.
  const settled = useDebounce(query.trim(), 300);
  const search = useMDBListSearch(settled, searchEnabled && browsing);
  const [lastFound, setLastFound] = useState<{ query: string; lists: MDBListListSummary[] }>();
  if (search.data && search.data.lists !== lastFound?.lists && settled) {
    setLastFound({ query: settled, lists: search.data.lists });
  }
  // A failed search keeps the last results; a new term shows "Searching…" until it answers.
  const fallback = search.isError ? lastFound?.lists : undefined;
  const found = settled && browsing ? (search.data?.lists ?? fallback ?? []) : [];

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
  const ownLists = useMemo(
    () =>
      (accountLists.data ?? []).filter((list) => !term || list.name.toLowerCase().includes(term)),
    [accountLists.data, term],
  );

  function choose(value: string) {
    const template = templates.find((entry) => entry.id === value);
    if (template) return onPick(templatePick(template, scopeKind));
    const own = accountLists.data?.find((entry) => mdblistAccountPickId(entry) === value);
    if (own) return onPick(mdblistAccountPick(own));
    const list = found.find((entry) => mdblistPickId(entry) === value);
    if (list) onPick(mdblistPick(list));
  }

  const cleaned = cleanMDBListLink(link);
  const linkInvalid = cleaned !== "" && !isMDBListLink(cleaned);
  // A private list syncs through the server's key, which the account lists come from.
  const privateHelp =
    scopeKind === "server" ? t("web.collections.templateGallery.pasteLinkPrivateServer") : null;
  const searchOn = searchEnabled || !browsing;

  return (
    <div className="grid gap-3">
      {ownOffered ? (
        <div
          role="group"
          aria-label={t("web.collections.templateGallery.listViews")}
          className="bg-muted inline-flex w-fit gap-1 rounded-lg p-1"
        >
          {(["own", "browse"] as const).map((entry) => (
            <button
              key={entry}
              type="button"
              aria-pressed={showing === entry}
              onClick={() => setView(entry)}
              className={cn(
                "text-muted-foreground hover:text-foreground inline-flex h-8 items-center gap-1.5 rounded-md px-3 text-[0.8125rem] font-medium",
                showing === entry && "bg-background text-foreground shadow-sm",
              )}
            >
              {entry === "own" ? (
                <>
                  <Lock aria-hidden className="size-3.5" />
                  {t("web.collections.templateGallery.viewOwnLists")}
                  {accountLists.data ? (
                    <span className="text-muted-foreground tabular-nums">
                      {accountLists.data.length.toLocaleString(formatLocale())}
                    </span>
                  ) : null}
                </>
              ) : (
                t("web.collections.templateGallery.viewBrowse")
              )}
            </button>
          ))}
        </div>
      ) : null}
      <div className="relative">
        <Search
          aria-hidden
          className="text-muted-foreground absolute start-3 top-1/2 size-4 -translate-y-1/2"
        />
        <Input
          type="search"
          aria-label={t("web.collections.templateGallery.searchLists")}
          aria-describedby={searchOn ? undefined : `${id}-off`}
          placeholder={
            !browsing
              ? t("web.collections.templateGallery.searchOwnLists")
              : searchEnabled
                ? t("web.collections.templateGallery.searchPlaceholder")
                : t("web.collections.templateGallery.searchOff")
          }
          disabled={!searchOn}
          className="h-11 ps-9"
          value={query}
          onChange={(event) => setQuery(event.target.value)}
        />
      </div>
      {searchOn ? null : (
        <p
          id={`${id}-off`}
          className="bg-muted/60 flex items-start gap-2.5 rounded-xl px-3 py-2.5 text-[0.8125rem]"
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
      {browsing && picks.length > 1 ? (
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
                  "border-border hover:bg-accent h-8 rounded-full border px-3 text-[0.8125rem] font-medium",
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
        {!browsing ? (
          <div>
            <GroupHeading title={yourMdblistLists()} help={yourMdblistListsServerHelp()} />
            {ownLists.map((list) => (
              <PickRow
                key={list.url}
                value={mdblistAccountPickId(list)}
                title={list.name}
                tag={list.private ? t("web.collections.templateGallery.listPrivate") : undefined}
                poster={
                  list.private ? (
                    <Lock aria-hidden className="text-muted-foreground size-3.5" />
                  ) : undefined
                }
                meta={accountListMeta(t, list)}
              />
            ))}
            {accountLists.isError ? (
              <p role="alert" className="text-destructive px-3.5 py-2.5 text-[0.8125rem]">
                {t("web.collections.templateGallery.accountListsFailed", {
                  detail: accountLists.error instanceof Error ? accountLists.error.message : "",
                }).trim()}
              </p>
            ) : accountLists.isPending ? (
              <p className="text-muted-foreground px-3.5 py-2.5 text-[0.8125rem]">
                {t("web.collections.templateGallery.loadingOwnLists")}
              </p>
            ) : term && ownLists.length === 0 ? (
              <p className="text-muted-foreground px-3.5 py-2.5 text-[0.8125rem]">
                {t("web.collections.templateGallery.noOwnMatches", { query: query.trim() })}
              </p>
            ) : null}
          </div>
        ) : null}
        {browsing && shown.length > 0 ? (
          <div>
            <GroupHeading title={popularPicks()} help={popularPicksHelp()} />
            {shown.map((template) => (
              <CollectionTemplateCard key={template.id} template={template} />
            ))}
          </div>
        ) : null}
        {browsing && settled ? (
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
              <p role="alert" className="text-destructive px-3.5 py-2.5 text-[0.8125rem]">
                {t("web.collections.templateGallery.searchFailed", {
                  detail: search.error instanceof Error ? search.error.message : "",
                }).trim()}
              </p>
            ) : search.isFetching && found.length === 0 ? (
              <p className="text-muted-foreground px-3.5 py-2.5 text-[0.8125rem]">
                {t("web.collections.templateGallery.searching")}
              </p>
            ) : search.data && found.length === 0 ? (
              <p className="text-muted-foreground px-3.5 py-2.5 text-[0.8125rem]">
                {t("web.collections.templateGallery.noMatches", { query: settled })}
              </p>
            ) : null}
          </div>
        ) : null}
        {browsing && shown.length === 0 && !settled ? (
          <p className="text-muted-foreground px-3.5 py-3 text-[0.8125rem]">
            {t("web.collections.templateGallery.noPicks")}
          </p>
        ) : null}
      </RadioGroupPrimitive.Root>

      <div className="grid gap-2">
        <label htmlFor={`${id}-link`} className="text-[0.875rem] font-semibold">
          {pasteMdblistLink()}
        </label>
        {/* Set on the wrapper so the start-side icon and the input's start padding stay
            on the same side. */}
        <div dir="ltr" className="relative">
          <Link2
            aria-hidden
            className="text-muted-foreground absolute start-3 top-1/2 size-4 -translate-y-1/2"
          />
          <Input
            id={`${id}-link`}
            type="url"
            inputMode="url"
            placeholder={MDBLIST_LINK_PLACEHOLDER}
            className="h-11 ps-9"
            aria-invalid={linkInvalid || undefined}
            aria-describedby={
              linkInvalid ? `${id}-link-error` : privateHelp ? `${id}-link-help` : undefined
            }
            value={link}
            onChange={(event) => onLinkChange(event.target.value)}
          />
        </div>
        {linkInvalid ? (
          <p id={`${id}-link-error`} className="text-destructive text-[0.78125rem]">
            {mdblistLinkInvalid()}
          </p>
        ) : privateHelp ? (
          <p id={`${id}-link-help`} className="text-muted-foreground text-[0.78125rem]">
            {privateHelp}
          </p>
        ) : null}
      </div>
    </div>
  );
}
