import { pageParam } from "./pages";
import type { PageRef, Surface } from "./types";

/**
 * Links into a Home rows page from elsewhere (a collection's editor):
 * `?add=collection:<library|user>:<id>` opens Add row on that collection,
 * `?edit=<rowId>` opens a row, `?return=<path>` is where to go back to.
 * Each page reads them once and then drops them from the address.
 */
export const ROW_LINK_PARAMS = ["add", "edit", "return"] as const;

/**
 * Navigation state on the `?return=` page after Add row from a link, so that
 * page can show which row is new.
 */
export interface AddedRowState {
  addedRow: {
    /** The new row on `page`. */
    id: string;
    surface: Surface;
    page: PageRef;
    /** Its place on `page`: new rows go to the bottom, so also the row count. */
    position: number;
  };
}

export interface RowLinkCollection {
  source: "library" | "user";
  id: string;
}

export interface RowLinks {
  /** "invalid" when `?add=` names something this page can't add. */
  add: RowLinkCollection | "invalid" | null;
  edit: string | null;
  returnTo: string | null;
}

const ADD_PATTERN = /^collection:(library|user):(.+)$/;

function parseAdd(value: string | null, surface: Surface): RowLinks["add"] {
  if (value === null) return null;
  const match = ADD_PATTERN.exec(value);
  const source = match?.[1] as RowLinkCollection["source"] | undefined;
  // Admin rows show server collections only.
  if (!match || !source || (surface === "admin" && source !== "library")) return "invalid";
  return { source, id: match[2]! };
}

/** The link parameters in `params`, or null when there are none. */
export function readRowLinks(params: URLSearchParams, surface: Surface): RowLinks | null {
  if (!ROW_LINK_PARAMS.some((name) => params.has(name))) return null;
  return {
    add: parseAdd(params.get("add"), surface),
    edit: params.get("edit") || null,
    returnTo: safeReturnPath(params.get("return")),
  };
}

const RETURN_PREFIXES = ["/admin/collections/", "/collections/"];
// eslint-disable-next-line no-control-regex
const CONTROL_CHARACTER = /[\u0000-\u001f\u007f]/;

function unsafe(path: string) {
  return (
    path.includes("\\") ||
    path.includes("//") ||
    CONTROL_CHARACTER.test(path) ||
    path.split(/[/?#]/).some((segment) => segment === "." || segment === "..")
  );
}

/**
 * `?return=` only ever leads back to a collection page on this site: it must
 * start with one of the collection prefixes, and neither it nor its decoded
 * form may hold a backslash, `//`, a control character or a `.`/`..` segment.
 * Anything else is ignored.
 */
export function safeReturnPath(value: string | null): string | null {
  if (!value || !RETURN_PREFIXES.some((prefix) => value.startsWith(prefix))) return null;
  let decoded: string;
  try {
    decoded = decodeURIComponent(value);
  } catch {
    return null;
  }
  return unsafe(value) || unsafe(decoded) ? null : value;
}

const HOME_ROWS_PATHS: Record<Surface, string> = {
  admin: "/admin/sections",
  profile: "/settings/home-screen",
};

/** A Home rows page, opened on `page`, with any link parameters. */
export function homeRowsPath(
  surface: Surface,
  page: PageRef,
  links: Partial<Record<(typeof ROW_LINK_PARAMS)[number], string>> = {},
): string {
  const params = new URLSearchParams({ page: pageParam(page), ...links });
  return `${HOME_ROWS_PATHS[surface]}?${params}`;
}
