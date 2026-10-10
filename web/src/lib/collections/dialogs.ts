import { PERSONAL_SCOPE, SERVER_SCOPE, type ScopeKind } from "./scope";

/** `?dialog=new` on a collections list opens the New collection picker, so a link can open it. */
export const NEW_COLLECTION_DIALOG = "new";

/** `?dialog=starter-packs` on the server list opens Starter packs. */
export const STARTER_PACKS_DIALOG = "starter-packs";

function setDialog(href: string, dialog: string | null) {
  const [path = "", query = ""] = href.split("?", 2);
  const params = new URLSearchParams(query);
  if (dialog) params.set("dialog", dialog);
  else params.delete("dialog");
  const search = params.toString();
  return search ? `${path}?${search}` : path;
}

/** A list URL with no dialog open over it. */
export function withoutDialog(href: string) {
  return setDialog(href, null);
}

/** The collections list with the New collection picker open over it. */
export function newCollectionPickerHref(scope: ScopeKind, libraryId?: number | null) {
  const list = (scope === "server" ? SERVER_SCOPE : PERSONAL_SCOPE).paths.list({ libraryId });
  return setDialog(list, NEW_COLLECTION_DIALOG);
}

/** The server list (`listHref`, the list as it was) with Starter packs open over it. */
export function starterPacksHref(listHref: string) {
  return setDialog(listHref, STARTER_PACKS_DIALOG);
}
