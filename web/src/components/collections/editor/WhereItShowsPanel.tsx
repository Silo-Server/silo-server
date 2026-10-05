import { useId, useState, type ReactNode } from "react";
import { Link } from "react-router";
import { useQueries } from "@tanstack/react-query";
import { AlertTriangle } from "lucide-react";

import { fetchAdminCollections } from "@/api/adminCollections";
import { adminKeys } from "@/hooks/queries/keys";
import {
  PERSONAL_TAB_HELP,
  SHOW_ON_TAB_LABEL,
  serverTabHelp,
  unshareWarning,
} from "@/lib/collections/copy";
import { SERVER_SCOPE, type CollectionDraft } from "@/lib/collections/scope";

import { HideCollectionDialog } from "../HideCollectionDialog";
import { focusLibrariesLine } from "../fields/librariesLineFocus";
import { ShowToOtherProfilesField } from "../ShowToOtherProfilesField";
import { ToggleRow } from "../fields/ToggleRow";

interface NamedLibrary {
  id: number;
  name: string;
}

/**
 * The shelf (group) a server collection sits on in each library's
 * Collections tab, and "pinned first" where Pin leads it: a shelf in Your
 * order, No heading included.
 */
function useShelves(collectionId: string | undefined, libraries: readonly NamedLibrary[]) {
  const lists = useQueries({
    queries: libraries.map((library) => ({
      queryKey: adminKeys.collections(library.id),
      queryFn: () => fetchAdminCollections(library.id),
      enabled: Boolean(collectionId),
      staleTime: 60 * 1000,
    })),
  });
  return libraries.map((library, index) => {
    const data = lists[index]?.data;
    const entry = data?.collections.find((collection) => collection.id === collectionId);
    const group = entry?.group_id
      ? data?.groups.find((candidate) => candidate.id === entry.group_id)
      : undefined;
    const leads = entry?.featured === true && (group?.default_sort_mode ?? "manual") === "manual";
    return `${library.name} › ${group?.name ?? "no heading"}${leads ? ", pinned first" : ""}`;
  });
}

function ServerFacts({
  collectionId,
  libraries,
}: {
  collectionId?: string;
  libraries: readonly NamedLibrary[];
}) {
  const shelves = useShelves(collectionId, libraries);
  const first = libraries[0];
  return (
    <dl className="grid grid-cols-[82px_minmax(0,1fr)] gap-x-3 gap-y-2 text-[13.5px]">
      <dt className="text-muted-foreground">Libraries</dt>
      <dd>
        {libraries.length > 0 ? libraries.map((library) => library.name).join(", ") : "None yet"}{" "}
        <button
          type="button"
          className="font-medium underline underline-offset-4"
          onClick={focusLibrariesLine}
        >
          Change
        </button>
      </dd>
      <dt className="text-muted-foreground">Shelf</dt>
      <dd>
        {collectionId ? shelves.join(", ") : "Lands with no heading"}
        {collectionId && first ? (
          <>
            {" "}
            <Link
              to={SERVER_SCOPE.paths.list({ view: "arrange", libraryId: first.id })}
              className="font-medium underline underline-offset-4"
            >
              Arrange
            </Link>
          </>
        ) : null}
      </dd>
    </dl>
  );
}

/** Rows show the collection: turning the Collections tab switch off asks first. */
export interface HideConfirm {
  rowCount: number;
  /** Where the rows are ("Home and the Kids page"). */
  places: string | null;
}

/**
 * Where the collection shows. Server: its libraries, its shelf in each, the
 * Collections tab switch and the rows that show it (`rows`). Personal: sharing
 * with the other profiles on the account (hidden on a single-profile account),
 * the Collections tab switch and the viewer's own rows that show it (`rows`).
 */
export function WhereItShowsPanel({
  scopeKind,
  collectionId,
  draft,
  onChange,
  libraries,
  otherProfileNames,
  savedShared,
  rows,
  hideConfirm,
}: {
  scopeKind: "server" | "personal";
  collectionId?: string;
  draft: CollectionDraft;
  onChange: (update: (draft: CollectionDraft) => CollectionDraft) => void;
  /** Server: the collection's libraries, named. */
  libraries: readonly NamedLibrary[];
  /** Personal: the account's other profiles; empty on a single-profile account. */
  otherProfileNames: readonly string[];
  /** Personal: whether the saved collection is shared. */
  savedShared: boolean;
  /** The rows that show it, and Add as a row. */
  rows?: ReactNode;
  /** Server: set when rows show it, so hiding it asks first. */
  hideConfirm?: HideConfirm | null;
}) {
  const id = useId();
  const [confirmingHide, setConfirmingHide] = useState(false);

  function setVisible(on: boolean) {
    onChange((current) => ({
      ...current,
      server: { ...current.server, visibility: on ? "visible" : "hidden" },
    }));
  }

  return (
    <section
      aria-labelledby={`${id}-heading`}
      data-panel="where"
      className="surface-panel grid content-start gap-5 rounded-[22px] p-5 sm:p-6"
    >
      <h2 id={`${id}-heading`} className="text-[17px] font-semibold">
        Where it shows
      </h2>
      {scopeKind === "server" && draft.server ? (
        <>
          <ServerFacts collectionId={collectionId} libraries={libraries} />
          <div className="border-border/70 border-t pt-5">
            <ToggleRow
              label={SHOW_ON_TAB_LABEL}
              help={serverTabHelp(libraries.map((library) => library.name))}
              checked={draft.server.visibility === "visible"}
              onCheckedChange={(on) => {
                // Rows that show it would keep a See all that can't open it.
                if (!on && hideConfirm && hideConfirm.rowCount > 0) setConfirmingHide(true);
                else setVisible(on);
              }}
            />
          </div>
          {rows ? <div className="border-border/70 border-t pt-5">{rows}</div> : null}
          <HideCollectionDialog
            open={confirmingHide}
            onOpenChange={setConfirmingHide}
            name={draft.name}
            libraryNames={libraries.map((library) => library.name)}
            rowCount={hideConfirm?.rowCount ?? 0}
            rowPlaces={hideConfirm?.places}
            onConfirm={() => setVisible(false)}
          />
        </>
      ) : null}
      {scopeKind === "personal" && draft.personal ? (
        <>
          {otherProfileNames.length > 0 ? (
            <ShowToOtherProfilesField
              checked={draft.personal.shared}
              onCheckedChange={(shared) =>
                onChange((current) => ({
                  ...current,
                  personal: { inLibraryTabs: false, ...current.personal, shared },
                }))
              }
            >
              {collectionId && savedShared && !draft.personal.shared ? (
                <p
                  role="note"
                  className="border-warning/50 bg-warning/10 flex items-start gap-2.5 rounded-xl border px-3 py-2.5 text-[13px]"
                >
                  <AlertTriangle aria-hidden className="text-warning mt-0.5 size-4 shrink-0" />
                  {unshareWarning(otherProfileNames)}
                </p>
              ) : null}
            </ShowToOtherProfilesField>
          ) : null}
          <ToggleRow
            label={SHOW_ON_TAB_LABEL}
            help={PERSONAL_TAB_HELP}
            checked={draft.personal.inLibraryTabs}
            onCheckedChange={(inLibraryTabs) =>
              onChange((current) => ({
                ...current,
                personal: { shared: false, ...current.personal, inLibraryTabs },
              }))
            }
          />
          {rows ? <div className="border-border/70 border-t pt-5">{rows}</div> : null}
        </>
      ) : null}
    </section>
  );
}
