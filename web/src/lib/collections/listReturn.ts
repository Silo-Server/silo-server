/**
 * The server list hands its exact URL (view, library, type, search, failed
 * filter) to the editor it opens, in the router's history state, so the
 * editor's Back and its leave-after-delete land on the same view. The state
 * never comes from a link or the address bar, and only the list's own path is
 * accepted.
 */
import { useLocation } from "react-router";

const LIST_PATH = "/admin/collections";

export interface ListReturnState {
  returnTo: string;
}

export function listReturnState(href: string): ListReturnState {
  return { returnTo: href };
}

function isListHref(value: unknown): value is string {
  return (
    typeof value === "string" &&
    (value === LIST_PATH || value.startsWith(`${LIST_PATH}?`)) &&
    !value.includes("\\")
  );
}

/** The list URL the editor was opened from, or `fallback` when it wasn't opened from the list. */
export function useListReturnPath(fallback: string): string {
  const { state } = useLocation();
  const returnTo = (state as Partial<ListReturnState> | null)?.returnTo;
  return isListHref(returnTo) ? returnTo : fallback;
}
