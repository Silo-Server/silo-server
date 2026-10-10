import { useScopeSnapshot } from "@/hooks/queries/collectionScope";
import { useProfiles } from "@/hooks/queries/profiles";
import { useCurrentProfile } from "@/hooks/useCurrentProfile";
import { useIsActingAdmin } from "@/hooks/useIsActingAdmin";
import { ownerName } from "@/lib/collections/personalOwnership";
import type { RowLinkCollection } from "@/lib/homeRows/rowLinks";
import {
  PERSONAL_SCOPE,
  SERVER_SCOPE,
  type CollectionScope,
  type EditorSnapshot,
  type ScopeKind,
} from "@/lib/collections/scope";

/** The collection a collection page shows. `libraryId` is the library it was opened in, if any. */
export interface CollectionPageTarget {
  scope: ScopeKind;
  id: string;
  libraryId?: number;
}

/**
 * What a collection page offers the viewer. `manage`: they may change it
 * (their own personal collection, or a server collection while acting as
 * admin). `read-only`: someone else's; `ownerName` is the personal
 * collection's creator, null for a server collection. The viewer can still
 * add a read-only `collection` to their own Home or the library pages of
 * `libraryIds` (the library a server collection was opened in, or the
 * libraries a personal one matches; none means every library). `none` while
 * that is still unknown, so nothing appears and then disappears.
 */
export type CollectionPageAccess =
  | { kind: "none" }
  | { kind: "manage"; scope: CollectionScope; id: string; snapshot?: EditorSnapshot }
  | {
      kind: "read-only";
      ownerName: string | null;
      collection: RowLinkCollection;
      libraryIds: readonly number[];
    };

const NONE: CollectionPageAccess = { kind: "none" };

export function useCollectionPageAccess(target: CollectionPageTarget | null): CollectionPageAccess {
  const actingAdmin = useIsActingAdmin();
  const { profile } = useCurrentProfile();
  const profiles = useProfiles();
  const server = useScopeSnapshot(SERVER_SCOPE, target?.id, {
    enabled: target?.scope === "server" && actingAdmin,
  });
  const personal = useScopeSnapshot(PERSONAL_SCOPE, target?.id, {
    enabled: target?.scope === "personal",
  });

  if (!target) return NONE;
  // Acting admin, never the account role: a restricted or non-primary profile
  // on an admin account browses server collections like any viewer.
  if (target.scope === "server") {
    return actingAdmin
      ? { kind: "manage", scope: SERVER_SCOPE, id: target.id, snapshot: server.data }
      : {
          kind: "read-only",
          ownerName: null,
          collection: { source: "library", id: target.id },
          libraryIds: target.libraryId ? [target.libraryId] : [],
        };
  }
  const snapshot = personal.data;
  if (!snapshot || !profile) return NONE;
  if (PERSONAL_SCOPE.isReadOnly(snapshot.view, profile.id)) {
    // Wait for the names, so the byline never shows a placeholder first.
    if (profiles.isPending) return NONE;
    return {
      kind: "read-only",
      ownerName: ownerName(profiles.data, snapshot.view.ownerProfileId ?? ""),
      collection: { source: "user", id: target.id },
      libraryIds: snapshot.view.libraryIds,
    };
  }
  return { kind: "manage", scope: PERSONAL_SCOPE, id: target.id, snapshot };
}
