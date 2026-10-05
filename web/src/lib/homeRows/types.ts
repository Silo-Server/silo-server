/**
 * The data the Home rows components read. Both surfaces (admin Home rows and
 * Settings > Home Screen) map their own rows into these shapes through an
 * adapter hook, so nothing under components/homeRows reads an account role or
 * an API type.
 */
import type { CollectionOption } from "@/hooks/queries/useAllUserCollections";
import type { PeekRequest } from "@/components/calm/usePeekLimiter";
import type { RowDraft } from "./rowDraft";

export type Surface = "admin" | "profile";

export type PageRef = { kind: "home" } | { kind: "library"; libraryId: number };

export interface HomeRowsPageOption {
  ref: PageRef;
  label: string;
  /** A library page's library type ("movies", "series", ...), when known. */
  libraryType?: string;
}

/** A library page a row can be added to. */
export interface LibraryPage {
  id: number;
  label: string;
  libraryType?: string;
}

export interface HomeRow {
  id: string;
  title: string;
  sectionType: string;
  config: Record<string, unknown>;
  itemLimit: number;
  hero: boolean;
  /** Admin: the row is enabled. Profile: the row is not hidden. */
  shown: boolean;
  /** A row the profile added itself ("Yours"). Always false on the admin surface. */
  own: boolean;
  /** A legacy Trakt row: the server refuses config changes and turning it back on. */
  legacyTrakt: boolean;
  /** Profile: the server's own name for a row this profile renamed. */
  renamedFrom?: string;
}

/** An open Edit row: the row as it was read, plus whatever the surface needs to save over it. */
export interface EditSession {
  row: HomeRow;
  /** Surface-private: the admin keeps the stored row and its version here. */
  token: unknown;
  /** What the row shows can't change here (a server row on Settings > Home Screen). */
  kindLocked?: boolean;
}

/** Thrown by `save` when the row changed since the edit session was read. */
export class RowChangedError extends Error {
  constructor() {
    super("This row changed since you opened it.");
    this.name = "RowChangedError";
  }
}

export interface HomeRowsCapabilities {
  /** Step 2 and Edit row show a live preview of the draft. */
  draftPreview: boolean;
  /** A row may be added to several library pages at once (library pages only). */
  libraryCopies: boolean;
}

/** The collections a collection row may show on this surface. */
export interface RowCollections {
  options: CollectionOption[];
  loading: boolean;
  /** Read again in the background, for example after a collection was made. */
  fetching?: boolean;
  failed: boolean;
  /** Where collections are made and changed on this surface. */
  href: string;
}

export type HomeRowsConflict = null | { scope: "page" | "row"; rowId?: string };

export interface HomeRowsAdapter {
  surface: Surface;
  page: PageRef;
  pages: HomeRowsPageOption[];
  /** Ignored while `pending`, so a write never lands on a page the user left. */
  setPage(ref: PageRef): void;
  status: "loading" | "ready" | "error";
  /** The read failure shown when `status` is "error". */
  error: string | null;
  canEdit: boolean;
  /** In display order, including an attempted order the server has not accepted. */
  rows: HomeRow[];
  /** A write, or the refetch after it, is still in flight. */
  pending: boolean;
  conflict: HomeRowsConflict;
  /** Refetches the page and drops any attempted order. */
  reload(): Promise<void>;
  /** Whether a reorder may start now. */
  canReorder: boolean;
  /**
   * The version a reorder is checked against. Captured when a drag starts and
   * handed back to `reorder`, so a refetch during the drag cannot change it.
   */
  orderToken: unknown;
  /** `movedId` names the one row the user moved, when it was a single move. */
  reorder(orderedIds: string[], orderToken?: unknown, movedId?: string): Promise<void>;
  setShown(id: string, shown: boolean): Promise<void>;
  setHero(id: string, hero: boolean): Promise<void>;
  capabilities: HomeRowsCapabilities;
  /** Adds a row at the bottom of the page. Rejects when the server refuses it. */
  create(draft: RowDraft): Promise<{ newIds: string[] }>;
  /** Reads the row as it is now, for Edit row. */
  openEdit(id: string): Promise<EditSession>;
  /** Re-reads a row after a save was refused, for a new session over its latest version. */
  reloadEdit(session: EditSession): Promise<EditSession>;
  /** Saves over the session's version; rejects with RowChangedError when it moved on. */
  save(session: EditSession, draft: RowDraft): Promise<void>;
  /** Where a shown row's poster peek comes from; null (or absent) keeps the row's icon. */
  peek?(row: HomeRow): PeekRequest | null;
  /** What the collection picker offers; without it the picker has nothing to offer. */
  collections?: RowCollections;
}
