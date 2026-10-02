import type { AdminUser } from "@/api/types";
import type { AdminUserEditor } from "@/api/v2/adminUsers";
import {
  effectiveAccessGroupID,
  policyInheritHints,
  savedUserPolicyInheritHints,
} from "@/components/UserPolicyFields";
import { useAccessGroups } from "@/hooks/queries/admin/accessGroups";
import { useAdminLibraries } from "@/hooks/queries/admin/libraries";

import { DownloadsPolicyCard } from "./DownloadsPolicyCard";
import type { AccessCardProps } from "./EditableCard";
import { LibraryAccessCard } from "./LibraryAccessCard";
import { PlaybackCard } from "./PlaybackCard";
import { RequestsCard } from "./RequestsCard";
import { SignInCard } from "./SignInCard";
import { inheritContextFor } from "./policySources";

/**
 * Access & limits: every setting of the account in five cards, each editing
 * in place. One card edits at a time (see CardEditingProvider).
 */
export function AccessTab({
  user,
  editor,
  manageable,
  policyManageable,
  available,
}: {
  user: AdminUser;
  editor: AdminUserEditor | undefined;
  manageable: boolean;
  /** Whether the viewer may change libraries, limits, downloads and requests. */
  policyManageable: boolean;
  available: boolean;
}) {
  const groups = useAccessGroups().data ?? [];
  const libraries = useAdminLibraries().data ?? [];
  const groupId = effectiveAccessGroupID(user.role, user.access_group_id);
  const props: AccessCardProps = {
    user,
    // Edit only against the account this page shows.
    editor: editor?.user.id === user.id ? editor : undefined,
    manageable,
    available,
    groups,
    libraries,
    ctx: inheritContextFor(user, groups),
    hints: savedUserPolicyInheritHints(user, policyInheritHints(groupId, groups)),
  };

  const policy = { ...props, manageable: policyManageable };

  return (
    <div className="space-y-4">
      {manageable && !policyManageable ? (
        <p role="note" className="text-muted-foreground text-sm">
          Only the server owner can change an admin&apos;s access and limits.
        </p>
      ) : null}
      <div className="grid items-start gap-4 lg:grid-cols-2">
        <div className="flex min-w-0 flex-col gap-4">
          <SignInCard {...props} />
          <LibraryAccessCard {...policy} />
          <DownloadsPolicyCard {...policy} />
        </div>
        <div className="flex min-w-0 flex-col gap-4">
          <PlaybackCard {...policy} />
          <RequestsCard {...policy} />
        </div>
      </div>
    </div>
  );
}
