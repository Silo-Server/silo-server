import { useId, useState, type ReactNode } from "react";
import { Link } from "react-router";
import { useQueries } from "@tanstack/react-query";
import { AlertTriangle } from "lucide-react";

import { fetchAdminCollections } from "@/api/adminCollections";
import { Button } from "@/components/ui/button";
import { adminKeys } from "@/hooks/queries/keys";
import {
  LIBRARIES_NOT_PICKED,
  NO_HEADING,
  PERSONAL_TAB_HELP,
  SHELF_AFTER_CREATE,
  SHOW_ON_TAB_LABEL,
  serverTabHelp,
  unshareWarning,
} from "@/lib/collections/copy";
import { SERVER_SCOPE, type CollectionDraft } from "@/lib/collections/scope";

import { HideCollectionDialog } from "../HideCollectionDialog";
import { focusLibrariesLine } from "../fields/librariesLineFocus";
import { SettingRow } from "../fields/SettingRow";
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
    return `${library.name} › ${group?.name ?? NO_HEADING}${leads ? ", pinned first" : ""}`;
  });
}

/** Strong text in a row's value: the libraries, the shelf. */
function Value({ children }: { children: ReactNode }) {
  return <span className="text-foreground/90 font-medium">{children}</span>;
}

/**
 * A server collection's Libraries and Shelf rows. Change moves focus to the
 * libraries control in the contents card; Arrange opens the first library's
 * shelves once the collection exists.
 */
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
    <>
      <SettingRow
        label="Libraries"
        value={
          libraries.length > 0 ? (
            <Value>{libraries.map((library) => library.name).join(", ")}</Value>
          ) : (
            LIBRARIES_NOT_PICKED
          )
        }
        action={
          <Button
            type="button"
            variant="outline"
            size="sm"
            aria-label="Change libraries"
            onClick={focusLibrariesLine}
          >
            Change
          </Button>
        }
      />
      <SettingRow
        label="Shelf"
        value={
          collectionId ? (
            <Value>{shelves.join(", ")}</Value>
          ) : (
            <>
              <Value>{NO_HEADING}</Value> · {SHELF_AFTER_CREATE}
            </>
          )
        }
        action={
          collectionId && first ? (
            <Button asChild variant="outline" size="sm">
              <Link to={SERVER_SCOPE.paths.list({ view: "arrange", libraryId: first.id })}>
                Arrange
              </Link>
            </Button>
          ) : null
        }
      />
    </>
  );
}

/** Rows show the collection: turning the Collections tab switch off asks first. */
export interface HideConfirm {
  rowCount: number;
  /** Where the rows are ("Home and the Kids page"). */
  places: string | null;
}

/**
 * Where the collection shows, as a settings list with one row each. Server:
 * its libraries, its shelf in each, the Collections tab switch and the rows
 * that show it (`rows`). Personal: sharing with the other profiles on the
 * account (hidden on a single-profile account), the Collections tab switch and
 * the viewer's own rows that show it (`rows`).
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
      className="surface-panel grid content-start gap-5 rounded-[22px] p-5 sm:p-6"
    >
      <h2 id={`${id}-heading`} className="text-[17px] font-semibold">
        Where it shows
      </h2>
      <div className="divide-border/70 -my-4 grid divide-y [&>*]:py-4">
        {scopeKind === "server" && draft.server ? (
          <>
            <ServerFacts collectionId={collectionId} libraries={libraries} />
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
          </>
        ) : null}
        {rows}
      </div>
      {scopeKind === "server" && draft.server ? (
        <HideCollectionDialog
          open={confirmingHide}
          onOpenChange={setConfirmingHide}
          name={draft.name}
          libraryNames={libraries.map((library) => library.name)}
          rowCount={hideConfirm?.rowCount ?? 0}
          rowPlaces={hideConfirm?.places}
          onConfirm={() => setVisible(false)}
        />
      ) : null}
    </section>
  );
}
