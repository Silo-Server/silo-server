import { useCallback, useEffect, useRef, useState } from "react";
import { useLocation, useNavigate, useSearchParams } from "react-router";
import { toast } from "sonner";
import type { CollectionOption } from "@/hooks/queries/useAllUserCollections";
import { pageLabel } from "@/lib/homeRows/pages";
import { draftForPreset, findRecipe, withCollection, type RowDraft } from "@/lib/homeRows/rowDraft";
import {
  homeRowsPath,
  readRowLinks,
  ROW_LINK_PARAMS,
  type AddedRowState,
  type RowLinks,
} from "@/lib/homeRows/rowLinks";
import type { EditSession, HomeRow, HomeRowsAdapter } from "@/lib/homeRows/types";
import type { RecipeCatalogResponse } from "@/lib/recipes";

/** Add row opened on a collection from a link: step 2, already filled in. */
export interface RowSeed {
  draft: RowDraft;
  /** The dialog's back link, when the link said where it came from. */
  back?: { label: string; onClick: () => void };
  /** Replaces the page's own after-add step when the link said where to go back to. */
  onAdded?: (newIds: string[]) => void;
}

/** What the Add row / Edit row dialog opens on: no session to add a row. */
interface RowDialogTarget {
  session: EditSession | null;
  seed?: RowSeed;
}

/**
 * The page's Add row / Edit row dialog. `key` changes on every open, so a link
 * that arrives while the dialog is open starts a fresh dialog on its target.
 */
export function useRowDialog() {
  const [dialog, setDialog] = useState<(RowDialogTarget & { key: number }) | null>(null);
  const opens = useRef(0);
  const setRowDialog = useCallback((target: RowDialogTarget | null) => {
    setDialog(target && { ...target, key: ++opens.current });
  }, []);
  return [dialog, setRowDialog] as const;
}

const CANT_ADD = "This collection can't be added here.";
const PAGE_LOCKED = "This page can't change right now.";
const ROW_GONE = "That row no longer exists.";
const NO_KINDS = "The kinds of rows didn't load, so Add row couldn't open.";
const NO_COLLECTIONS = "Collections didn't load, so Add row couldn't open.";

/** Every link parameter's value, to notice a new link. Empty when there is none. */
function linkKey(params: URLSearchParams) {
  return ROW_LINK_PARAMS.some((name) => params.has(name))
    ? ROW_LINK_PARAMS.map((name) => params.get(name) ?? "").join("\n")
    : "";
}

/**
 * Follows `?add=`, `?edit=` and `?return=` on a Home rows page (see
 * `lib/homeRows/rowLinks`). The links are read once and dropped from the
 * address. The history state the link arrived with goes back to `?return=`
 * (the collection editor keeps the list it came from there). Nothing opens
 * until `settled` says the page's rows and whether it can change are known;
 * Add row then also waits for the page's collection options and opens only on
 * a collection among them.
 */
export function useRowLinks({
  adapter,
  catalog,
  catalogFailed,
  settled,
  onAdd,
  onEdit,
}: {
  adapter: HomeRowsAdapter;
  catalog: RecipeCatalogResponse | undefined;
  catalogFailed: boolean;
  settled: boolean;
  onAdd: (seed: RowSeed) => void;
  onEdit: (row: HomeRow) => void;
}) {
  const navigate = useNavigate();
  const location = useLocation();
  const [searchParams, setSearchParams] = useSearchParams();
  const key = linkKey(searchParams);
  const [read, setRead] = useState<{ key: string; links: RowLinks | null; state: unknown }>(() => ({
    key,
    links: key ? readRowLinks(searchParams, adapter.surface) : null,
    state: location.state,
  }));
  if (key !== read.key) {
    // A new link: read it. When the address then drops it, keep the link already read.
    setRead(
      key
        ? { key, links: readRowLinks(searchParams, adapter.surface), state: location.state }
        : { ...read, key },
    );
  }
  const { links, state: arrivedWith } = read;
  const handled = useRef<RowLinks | null>(null);
  // The page as it is now, for an add that lands after rows changed under the dialog.
  const latestRows = useRef(adapter.rows);
  useEffect(() => {
    latestRows.current = adapter.rows;
  }, [adapter.rows]);

  useEffect(() => {
    if (!key) return;
    setSearchParams(
      (current) => {
        const next = new URLSearchParams(current);
        for (const name of ROW_LINK_PARAMS) next.delete(name);
        return next;
      },
      { replace: true },
    );
  }, [key, setSearchParams]);

  useEffect(() => {
    if (!links || handled.current === links || !settled) return;
    const { add, edit, returnTo } = links;
    const collections = adapter.collections;

    function seedFor(option: CollectionOption, draft: RowDraft): RowSeed {
      if (!returnTo) return { draft };
      const { surface, page, pages } = adapter;
      const where = page.kind === "home" ? "Home" : `the ${pageLabel(page, pages)} page`;
      // Home rows was a detour from `returnTo`: replace it, so Back doesn't return to it.
      const carried = typeof arrivedWith === "object" ? arrivedWith : null;
      const back = () => navigate(returnTo, { replace: true, state: carried });
      return {
        draft,
        back: { label: `Back to ${option.title}`, onClick: back },
        onAdded: ([id]) => {
          if (!id) return back();
          // New rows go to the bottom; the page may not show this one yet.
          const rows = latestRows.current;
          const at = rows.findIndex((row) => row.id === id);
          const total = at === -1 ? rows.length + 1 : rows.length;
          const position = at === -1 ? total : at + 1;
          const added: AddedRowState = { addedRow: { id, surface, page, position } };
          navigate(returnTo, { replace: true, state: { ...carried, ...added } });
          toast.success(`Added to ${where} as row ${position} of ${total}`, {
            action: {
              label: "Move it",
              onClick: () => navigate(homeRowsPath(surface, page, { edit: id })),
            },
          });
        },
      };
    }

    // Acts on the link once; a return without acting waits for the next render.
    function finish(action: () => void) {
      handled.current = links;
      action();
    }
    const refuse = (message: string) => finish(() => toast.error(message));

    if (add === "invalid") {
      refuse(CANT_ADD);
    } else if (add) {
      if (!adapter.canEdit) return refuse(PAGE_LOCKED);
      if (!catalog) return catalogFailed ? refuse(NO_KINDS) : undefined;
      // Options read before the collection was made, or deleted, are refreshing.
      if (collections?.loading || collections?.fetching) return;
      // A failed refresh leaves the old options, which may hold a collection since hidden or deleted.
      if (collections?.failed) return refuse(NO_COLLECTIONS);
      const def = findRecipe(catalog, "collection");
      const option = collections?.options.find(
        (candidate) => candidate.id === add.id && candidate.source === add.source,
      );
      if (option && def) {
        const draft = withCollection(draftForPreset(def, def.presets[0]), option);
        return finish(() => onAdd(seedFor(option, draft)));
      }
      refuse(CANT_ADD);
    } else if (edit) {
      const row = adapter.rows.find((candidate) => candidate.id === edit);
      if (!adapter.canEdit) refuse(PAGE_LOCKED);
      else if (row) finish(() => onEdit(row));
      else refuse(ROW_GONE);
    } else {
      handled.current = links;
    }
  }, [links, arrivedWith, settled, adapter, catalog, catalogFailed, navigate, onAdd, onEdit]);
}
