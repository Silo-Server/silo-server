import { Check, CircleAlert } from "lucide-react";

import ViewTransitionLink from "@/components/ViewTransitionLink";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  useAdminPluginPermissions,
  useApprovePluginPermissions,
  type PluginPermissions,
} from "@/hooks/queries/admin/plugins";

import { DetailPanel } from "./PluginDetailSections";

export const PLUGIN_PERMISSIONS_PANEL_ID = "plugin-permissions";

const EVENT_ACCESS: Record<string, { label: string; detail: string }> = {
  admin: {
    label: "Admin activity",
    detail:
      "Playback sessions, library scans and admin jobs, including the accounts, devices and network addresses they name.",
  },
  user_data: {
    label: "Profiles' personal data",
    detail: "The notifications Silo adds to each profile's inbox.",
  },
};

function sameScopes(a: string[], b: string[]): boolean {
  return a.length === b.length && a.every((scope) => b.includes(scope));
}

/** Whether a key with exactly the required scopes is already issued. */
function keyMatches(permissions: PluginPermissions): boolean {
  const scopes = permissions.host_api_scopes;
  return permissions.host_api_key.issued && sameScopes(scopes.approved, scopes.required);
}

/** The approval can change something: the plugin asks for more, or its key carries other scopes. */
function approvalAvailable(permissions: PluginPermissions): boolean {
  return permissions.pending || (permissions.host_api_key.issued && !keyMatches(permissions));
}

function ApprovalState({ approved }: { approved: boolean }) {
  return approved ? (
    <span className="text-muted-foreground inline-flex shrink-0 items-center gap-1 text-[12.5px]">
      <Check aria-hidden="true" className="size-3.5" />
      Approved
    </span>
  ) : (
    <span className="text-warning inline-flex shrink-0 items-center gap-1 text-[12.5px]">
      <CircleAlert aria-hidden="true" className="size-3.5" />
      Not approved
    </span>
  );
}

/**
 * What an installed plugin receives and may call on this server, with an
 * Approve button while it asks for more than an administrator approved.
 * Renders nothing for a plugin that asks for nothing.
 */
export function PluginPermissionsPanel({ installationId }: { installationId: number }) {
  const query = useAdminPluginPermissions(installationId);
  const approve = useApprovePluginPermissions();
  const permissions = query.data;

  if (query.isError) {
    return (
      <DetailPanel
        id={PLUGIN_PERMISSIONS_PANEL_ID}
        title="Permissions"
        description="Silo couldn't load this plugin's permissions. Try again in a moment."
      />
    );
  }
  if (!permissions) return null;

  const events = permissions.event_access;
  const scopes = permissions.host_api_scopes;
  const key = permissions.host_api_key;
  if (events.required.length === 0 && scopes.requested.length === 0 && !key.issued) return null;

  const canApprove = approvalAvailable(permissions);
  const issuesKey = canApprove && scopes.required.length > 0 && !keyMatches(permissions);

  return (
    <DetailPanel
      id={PLUGIN_PERMISSIONS_PANEL_ID}
      title="Permissions"
      badge={
        permissions.pending ? (
          <Badge variant="outline" className="border-warning/40 text-warning text-[11px]">
            Needs approval
          </Badge>
        ) : null
      }
      description={
        permissions.pending
          ? "This plugin asks for more than you've approved. Until you approve, it keeps only what was approved before."
          : "What this plugin receives from Silo and what it can call on this server."
      }
    >
      <ul className="divide-y rounded-xl border">
        {events.required.map((access) => {
          const known = EVENT_ACCESS[access];
          return (
            <li key={access} className="flex items-start justify-between gap-4 p-3.5">
              <div className="min-w-0 space-y-0.5">
                <p className="text-sm font-medium">{known?.label ?? access}</p>
                {known ? <p className="text-muted-foreground text-[13px]">{known.detail}</p> : null}
              </div>
              <ApprovalState approved={events.approved.includes(access)} />
            </li>
          );
        })}
        {scopes.required.length > 0 || key.issued ? (
          <li className="space-y-2 p-3.5">
            <div className="space-y-0.5">
              <p className="text-sm font-medium">API access</p>
              <p className="text-muted-foreground text-[13px]">
                An API key with these scopes, which acts as the administrator who approved it. It
                appears on the{" "}
                <ViewTransitionLink to="/admin/api-keys" className="underline underline-offset-4">
                  API keys page
                </ViewTransitionLink>
                , where you can revoke it.
              </p>
            </div>
            <ul className="space-y-1.5">
              {[...new Set([...scopes.required, ...scopes.approved])].map((scope) => (
                <li key={scope} className="flex items-center justify-between gap-4">
                  <span className="font-mono text-[12.5px]">{scope}</span>
                  {scopes.required.includes(scope) ? (
                    <ApprovalState approved={scopes.approved.includes(scope)} />
                  ) : (
                    <span className="text-muted-foreground shrink-0 text-[12.5px]">
                      No longer requested
                    </span>
                  )}
                </li>
              ))}
            </ul>
            {key.issued && !key.usable ? (
              <p className="text-warning text-[13px]">
                The plugin's API key no longer works: the account it acts as is no longer an enabled
                administrator, or this server can't read it. Approving issues a new key.
              </p>
            ) : null}
          </li>
        ) : null}
      </ul>

      {scopes.unsupported.length > 0 ? (
        <p className="text-muted-foreground mt-3 text-[13px]">
          This server doesn't offer{" "}
          {scopes.unsupported.map((scope, index) => (
            <span key={scope}>
              {index > 0 ? ", " : null}
              <span className="font-mono text-[12.5px]">{scope}</span>
            </span>
          ))}
          , so the plugin won't get {scopes.unsupported.length === 1 ? "it" : "them"}.
        </p>
      ) : null}

      {canApprove ? (
        <div className="mt-4 flex flex-wrap items-center gap-3">
          <Button
            size="sm"
            disabled={approve.isPending}
            onClick={() => approve.mutate(installationId, permissions.etag)}
          >
            {approve.isPending ? "Approving..." : "Approve"}
          </Button>
          {issuesKey ? (
            <span className="text-muted-foreground text-[13px]">
              Approving issues an API key that acts as your account.
            </span>
          ) : null}
        </div>
      ) : null}
    </DetailPanel>
  );
}
