import { useMemo, useState } from "react";
import { Link } from "react-router";
import { toast } from "sonner";

import type { RequestSettings } from "@/api/types";
import { isRequestEditorConflict } from "@/api/v2/adminRequests";
import { EditorConflict } from "@/components/admin/EditorConflict";
import { SettingsPageHeader } from "@/components/settings/SettingsPageHeader";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { useAdminPluginInstallations } from "@/hooks/queries/admin/plugins";
import {
  useAdminRequestCapabilities,
  useRequestIntegrations,
  useRequestRoutes,
  useRequestSettings,
  useUpdateRequestSettings,
} from "@/hooks/queries/admin/requests";
import { useReportUnsavedChanges } from "@/hooks/useUnsavedChanges";
import { SaveBar } from "@/components/SaveBar";

import { FieldGroup } from "./FieldGroup";
import { RequestRoutingGroup } from "./RequestRouting";
import { RequestServersGroup } from "./RequestServers";
import { requestRouterInstallations } from "./requestServerModel";
import { SETTINGS_BUTTON, SettingField, SettingFieldRow, SettingFieldStatus } from "./SettingField";
import { useStagedDraft } from "./useStagedDraft";

interface GeneralDraft {
  requests_enabled: boolean;
  auto_approve: boolean;
  max_requests: string;
  window_days: string;
  force_dual_quality: boolean;
  /** Absent from servers without watchlist requests; the control hides. */
  watchlist_requests: boolean | undefined;
}

function generalDraft(settings: RequestSettings): GeneralDraft {
  return {
    requests_enabled: settings.requests_enabled,
    auto_approve: settings.global_auto_approval_enabled,
    max_requests: String(settings.global_max_requests),
    window_days: String(settings.global_window_days),
    force_dual_quality: settings.force_dual_quality,
    watchlist_requests: settings.watchlist_requests,
  };
}

function generalChanges(draft: GeneralDraft, base: GeneralDraft): number {
  return (Object.keys(draft) as (keyof GeneralDraft)[]).filter((key) => draft[key] !== base[key])
    .length;
}

function wholeNumber(value: string, min: number): number | null {
  const trimmed = value.trim();
  if (trimmed === "") return null;
  const n = Number(trimmed);
  return Number.isInteger(n) && n >= min ? n : null;
}

/** Help text for the General group's defaults that groups and accounts can override. */
function OverridableDefault() {
  return (
    <>
      The server-wide default.{" "}
      <Link to="/admin/access-groups" className="text-foreground underline underline-offset-2">
        Access groups
      </Link>{" "}
      and accounts can override this.
    </>
  );
}

function PageSkeleton() {
  return (
    <div className="space-y-6" role="status" aria-label="Loading request settings">
      <Skeleton className="h-8 w-48" />
      <Skeleton className="h-40 w-full" />
      <Skeleton className="h-40 w-full" />
      <span className="sr-only">Loading request settings</span>
    </div>
  );
}

/**
 * Settings → Requests: whether and how people request titles, the servers
 * requests go to, and the rules that pick a server for each title.
 */
export default function RequestsSettings() {
  const capabilities = useAdminRequestCapabilities();
  if (capabilities.isLoading) return <PageSkeleton />;
  if (!capabilities.data?.available || !capabilities.data.guarded_configuration) {
    return (
      <div className="flex flex-col gap-7">
        <SettingsPageHeader title="Requests" />
        <p className="text-muted-foreground text-sm">
          Request settings are not available on this server.
        </p>
      </div>
    );
  }
  return <RequestsSettingsContent routing={capabilities.data.routing} />;
}

/** routing: whether the server offers routing rules; without them the page
 * leaves them out rather than calling the route endpoints. */
function RequestsSettingsContent({ routing }: { routing: boolean }) {
  const settingsQuery = useRequestSettings();
  const routesQuery = useRequestRoutes(routing);
  const serversQuery = useRequestIntegrations();
  const installationsQuery = useAdminPluginInstallations();
  const updateSettings = useUpdateRequestSettings();
  const installations = useMemo(
    () => requestRouterInstallations(installationsQuery.data ?? []),
    [installationsQuery.data],
  );

  const general = useStagedDraft(settingsQuery.data, generalDraft, generalChanges);
  const [generalConflict, setGeneralConflict] = useState(false);
  const [saving, setSaving] = useState(false);

  // A limit of 0 lets only accounts with their own limit request.
  const maxRequests = general.draft ? wholeNumber(general.draft.max_requests, 0) : null;
  const windowDays = general.draft ? wholeNumber(general.draft.window_days, 1) : null;
  const baseGeneral = general.base ? generalDraft(general.base) : undefined;
  const maxInvalid =
    maxRequests === null && general.draft?.max_requests !== baseGeneral?.max_requests;
  const windowInvalid =
    windowDays === null && general.draft?.window_days !== baseGeneral?.window_days;

  const dirtyCount = general.changes;
  useReportUnsavedChanges(dirtyCount > 0);
  const saveable = general.changes > 0 && !generalConflict && !maxInvalid && !windowInvalid;

  function editGeneral(change: Partial<GeneralDraft>) {
    general.update((current) => ({ ...current, ...change }));
  }

  async function saveGeneral() {
    const { base, draft } = general;
    if (!base || !draft || general.changes === 0 || generalConflict) return;
    if (maxInvalid || windowInvalid) return;
    try {
      const saved = await updateSettings.mutateAsync({
        requests_enabled: draft.requests_enabled,
        global_auto_approval_enabled: draft.auto_approve,
        // An untouched stored value is sent back as it is, even one the
        // controls would not offer.
        global_max_requests: maxRequests ?? base.global_max_requests,
        global_window_days: windowDays ?? base.global_window_days,
        force_dual_quality: draft.force_dual_quality,
        watchlist_requests: draft.watchlist_requests,
        updated_at: base.updated_at,
        etag: base.etag,
      });
      general.adopt(saved);
    } catch (error) {
      if (isRequestEditorConflict(error)) setGeneralConflict(true);
    }
  }

  // The save bar covers General only; routing saves as it goes.
  async function saveAll() {
    setSaving(true);
    try {
      await saveGeneral();
    } finally {
      setSaving(false);
    }
  }

  // A discarded draft goes back to its record and then, being clean, follows
  // the query to whatever is newest, so a conflict it hit no longer applies.
  function discardAll() {
    general.reset();
    setGeneralConflict(false);
  }

  async function reloadGeneral() {
    const result = await settingsQuery.refetch();
    if (result.data && !result.isError) {
      general.adopt(result.data);
      setGeneralConflict(false);
    } else {
      toast.error(
        result.error instanceof Error
          ? `Couldn't reload request settings: ${result.error.message}`
          : "Couldn't reload request settings.",
      );
    }
  }

  const draft = general.draft;
  const servers = serversQuery.data ?? [];
  const routes = routesQuery.data ?? [];

  return (
    <div className="flex h-full flex-col">
      <SettingsPageHeader
        title="Requests"
        className="mb-8"
        actions={
          <Button asChild variant="outline" size="sm">
            <Link to="/admin/requests">Request queue</Link>
          </Button>
        }
      />

      <div className="flex-1 space-y-5">
        {/* Locked while the save bar is saving: a save adopts what the server
            returned, which would drop an edit made in the meantime. */}
        <fieldset disabled={saving} className="min-w-0">
          <FieldGroup label="General" dirty={general.changes > 0}>
            {settingsQuery.isLoading ? (
              <div className="space-y-2 py-3.5">
                <Skeleton className="h-9 w-full" />
                <Skeleton className="h-9 w-full" />
              </div>
            ) : !draft || !baseGeneral ? (
              <p className="text-destructive py-3.5 text-sm">
                Request settings could not be loaded.
              </p>
            ) : (
              <>
                <SettingField
                  label="Allow requests"
                  type="toggle"
                  description="People can ask for movies and series the server does not have."
                  value={String(draft.requests_enabled)}
                  onChange={(value) => editGeneral({ requests_enabled: value === "true" })}
                  dirty={draft.requests_enabled !== baseGeneral.requests_enabled}
                />
                <SettingField
                  label="Approval"
                  type="select"
                  options={[
                    { value: "auto", label: "Approve automatically" },
                    { value: "admin", label: "An admin approves" },
                  ]}
                  description={<OverridableDefault />}
                  value={draft.auto_approve ? "auto" : "admin"}
                  onChange={(value) => editGeneral({ auto_approve: value === "auto" })}
                  dirty={draft.auto_approve !== baseGeneral.auto_approve}
                />
                <SettingField
                  label="Request limit"
                  type="number"
                  unit="requests"
                  description={
                    <>
                      How many titles one account can request in the window below. Declined and
                      failed requests don&apos;t count. At 0, only accounts with a group or account
                      limit of their own can request. <OverridableDefault />
                    </>
                  }
                  value={draft.max_requests}
                  onChange={(value) => editGeneral({ max_requests: value })}
                  dirty={draft.max_requests !== baseGeneral.max_requests}
                  status={
                    maxInvalid ? (
                      <SettingFieldStatus tone="warn">
                        Use a whole number, 0 or more.
                      </SettingFieldStatus>
                    ) : undefined
                  }
                />
                <SettingField
                  label="Limit window"
                  type="number"
                  unit="days"
                  value={draft.window_days}
                  onChange={(value) => editGeneral({ window_days: value })}
                  dirty={draft.window_days !== baseGeneral.window_days}
                  status={
                    windowInvalid ? (
                      <SettingFieldStatus tone="warn">Use at least 1 day.</SettingFieldStatus>
                    ) : undefined
                  }
                />
                <SettingField
                  label="Also request a 4K version of every title"
                  type="toggle"
                  description="Normally a 4K version is requested only for people whose playback limit allows 4K, and only when a server takes 4K. With this on, every request also asks for 4K."
                  value={String(draft.force_dual_quality)}
                  onChange={(value) => editGeneral({ force_dual_quality: value === "true" })}
                  dirty={draft.force_dual_quality !== baseGeneral.force_dual_quality}
                />
                {draft.watchlist_requests !== undefined ? (
                  <SettingField
                    label="Request titles added to a watchlist"
                    type="toggle"
                    description="When someone adds a title the library doesn't have to their watchlist, it is requested for them, as if they had pressed Request. Each profile can turn this off for itself."
                    value={String(draft.watchlist_requests)}
                    onChange={(value) => editGeneral({ watchlist_requests: value === "true" })}
                    dirty={draft.watchlist_requests !== baseGeneral.watchlist_requests}
                  />
                ) : null}
                {generalConflict ? (
                  <div className="py-3.5">
                    <EditorConflict onReload={reloadGeneral} />
                  </div>
                ) : null}
              </>
            )}
          </FieldGroup>
        </fieldset>

        <RequestServersGroup
          servers={servers}
          serversLoading={serversQuery.isLoading}
          serversError={serversQuery.isError && !serversQuery.data}
          installations={installations}
          installationsLoading={installationsQuery.isLoading}
          routes={routes}
          routing={routing}
        />

        {routing ? (
          <RequestRoutingGroup
            routes={routes}
            routesLoading={routesQuery.isLoading}
            routesFetching={routesQuery.isFetching}
            routesError={routesQuery.isError && !routesQuery.data}
            serversLoading={serversQuery.isLoading}
            allServers={servers}
            installations={installations}
            requestsEnabled={general.base?.requests_enabled}
            forceDual={general.base?.force_dual_quality ?? false}
          />
        ) : null}

        <FieldGroup label="Related">
          <SettingFieldRow
            label="Group and account limits"
            description="Give an access group or one account its own approval and limit. An account's own setting wins, then its group's, then the defaults above."
          >
            <Button asChild variant="outline" size="sm" className={SETTINGS_BUTTON}>
              <Link to="/admin/access-groups">Access groups</Link>
            </Button>
            <Button asChild variant="outline" size="sm" className={SETTINGS_BUTTON}>
              <Link to="/admin/users">Users</Link>
            </Button>
          </SettingFieldRow>
          <SettingFieldRow
            label="Request notifications"
            description="Announce submitted, approved, declined, and fulfilled requests on Discord or a webhook."
          >
            <Button asChild variant="outline" size="sm" className={SETTINGS_BUTTON}>
              <Link to="/admin/settings/notifications">Notifications</Link>
            </Button>
          </SettingFieldRow>
          <SettingFieldRow
            label="Autoscan"
            description="Autoscan can reuse these servers to import downloads as soon as they finish."
          >
            <Button asChild variant="outline" size="sm" className={SETTINGS_BUTTON}>
              <Link to="/admin/libraries?tab=autoscan">Autoscan</Link>
            </Button>
          </SettingFieldRow>
        </FieldGroup>
      </div>

      <SaveBar
        dirtyCount={dirtyCount}
        onSave={() => void saveAll()}
        onDiscard={discardAll}
        isSaving={saving}
        canSave={saveable}
      />
    </div>
  );
}
