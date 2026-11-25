import { useState } from "react";
import { KeyRound, Loader2, RefreshCw } from "lucide-react";
import { toast } from "sonner";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import { SettingsGroup } from "@/components/settings/SettingsGroup";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { v2ErrorText } from "@/api/v2/request";
import {
  useAdminAppKeys,
  useAppKeyCapability,
  type AdminAppDeviceKey,
  type DevicePreapproval,
} from "@/hooks/queries/appKeys";
import { formatDateTime } from "@/lib/datetime";
import { TOUCH_TARGETS } from "@/lib/touchTargets";
import { useT } from "@/i18n";
import { AppKeyRow, PreapprovalRow } from "@/components/appKeys/AppKeyRow";

type Selection =
  | { kind: "remove"; key: AdminAppDeviceKey }
  | { kind: "deny"; key: AdminAppDeviceKey }
  | { kind: "person" }
  | { kind: "withdraw"; preapproval: DevicePreapproval };

const P = "web.admin.users.detail.appKeys";

/**
 * An account's Silo app devices on the administrator's user page: its account
 * key, the devices waiting for approval, its devices, and the sign-ins
 * approved in advance. `manageable` mirrors the Owner rules the server
 * applies; without it the lists show no buttons.
 */
export function AdminAppKeysPanel({
  user,
  manageable,
}: {
  user: { id: number; username: string };
  manageable: boolean;
}) {
  const t = useT();
  const { keys, preapprovals, revokeDevice, revokePersonKey, deny, withdraw } = useAdminAppKeys(
    user.id,
  );
  const capability = useAppKeyCapability();
  const [selection, setSelection] = useState<Selection | null>(null);
  const [error, setError] = useState<string | null>(null);
  const pending =
    revokeDevice.isPending || revokePersonKey.isPending || deny.isPending || withdraw.isPending;
  const username = user.username;

  function open(next: Selection) {
    setError(null);
    setSelection(next);
  }

  async function confirm() {
    if (!selection || pending) return;
    setError(null);
    try {
      switch (selection.kind) {
        case "remove":
          await revokeDevice.mutateAsync(selection.key.id);
          toast.success(t(`${P}.removed`, { device: selection.key.name }));
          break;
        case "deny":
          await deny.mutateAsync(selection.key.id);
          toast.success(t(`${P}.denied`, { device: selection.key.name }));
          break;
        case "person":
          await revokePersonKey.mutateAsync();
          toast.success(t(`${P}.personKeyRemoved`));
          break;
        case "withdraw":
          await withdraw.mutateAsync(selection.preapproval.id);
          toast.success(t(`${P}.withdrawn`));
          break;
      }
      setSelection(null);
    } catch (err) {
      setError(v2ErrorText(err, t(`${P}.actionFailed`)));
    }
  }

  const data = keys.data;
  const devices = data?.devices ?? [];
  const waiting = devices.filter((key) => key.status === "pending" && !key.identity_gone);
  const enrolled = devices.filter((key) => key.status !== "pending" || key.identity_gone);
  const approved = preapprovals.data ?? [];
  const empty = data && !data.person_key && devices.length === 0 && approved.length === 0;
  const loading = keys.isPending || preapprovals.isPending;
  const failed = keys.isError || preapprovals.isError;
  const refetch = () => {
    void keys.refetch();
    void preapprovals.refetch();
  };

  const dialog = dialogText(t, selection, username);
  return (
    <section id="devices" className="scroll-mt-24 space-y-6">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0 space-y-2">
          <h2 className="text-lg font-semibold tracking-tight">{t(`${P}.title`)}</h2>
          <p className="text-muted-foreground max-w-2xl text-sm leading-relaxed">
            {t(`${P}.description`, { username })}
          </p>
        </div>
        <Button
          size="sm"
          variant="outline"
          onClick={refetch}
          disabled={keys.isFetching || preapprovals.isFetching}
          aria-label={t(`${P}.refreshLabel`)}
        >
          <RefreshCw className={keys.isFetching ? "animate-spin" : undefined} aria-hidden="true" />
          {t("web.settings.sessions.refresh")}
        </Button>
      </div>
      {capability.data?.state === "disabled" && (
        <p className="surface-panel-subtle text-muted-foreground p-3 text-sm">{t(`${P}.off`)}</p>
      )}
      {loading ? (
        <p role="status" className="text-muted-foreground flex items-center gap-2 text-sm">
          <Loader2 className="size-4 animate-spin" aria-hidden="true" />
          {t(`${P}.loading`)}
        </p>
      ) : failed ? (
        <div role="alert" className="surface-panel-raised space-y-3 p-5">
          <p>{t(`${P}.loadFailed`)}</p>
          <Button variant="outline" onClick={refetch} disabled={keys.isFetching}>
            {t("common.retry")}
          </Button>
        </div>
      ) : empty ? (
        <p className="text-muted-foreground py-2 text-sm">{t(`${P}.empty`)}</p>
      ) : (
        <>
          <SettingsGroup title={t(`${P}.personKey.title`)} flush>
            <div className="flex flex-wrap items-center gap-3 py-2">
              <KeyRound className="text-muted-foreground size-4 shrink-0" aria-hidden="true" />
              <p className="min-w-0 flex-1 basis-60 text-sm">
                {data?.person_key
                  ? t(`${P}.personKey.description`, {
                      username,
                      date: formatDateTime(data.person_key.enrolled_at, { seconds: false }),
                    })
                  : t(`${P}.personKey.none`, { username })}
                {data?.person_key?.provider_bound && (
                  <Badge variant="outline" className="ms-2">
                    {t("web.settings.appKeys.row.provider")}
                  </Badge>
                )}
              </p>
              {manageable && data?.person_key && (
                <Button size="sm" variant="outline" onClick={() => open({ kind: "person" })}>
                  {t(`${P}.personKey.remove`)}
                </Button>
              )}
            </div>
          </SettingsGroup>
          {waiting.length > 0 && (
            <SettingsGroup
              title={t(`${P}.waiting.title`)}
              description={t(`${P}.waiting.description`, { username })}
              flush
            >
              <ul className="mt-3 space-y-2">
                {waiting.map((key) => (
                  <AppKeyRow
                    key={key.id}
                    appKey={key}
                    actions={
                      manageable
                        ? [
                            {
                              label: t("web.settings.appKeys.row.deny"),
                              ariaLabel: t("web.settings.appKeys.row.denyLabel", {
                                device: key.name,
                              }),
                              onClick: () => open({ kind: "deny", key }),
                            },
                          ]
                        : undefined
                    }
                  />
                ))}
              </ul>
            </SettingsGroup>
          )}
          {enrolled.length > 0 && (
            <SettingsGroup title={t(`${P}.devices.title`)} flush>
              <ul className="mt-3 space-y-2">
                {enrolled.map((key) => (
                  <AppKeyRow
                    key={key.id}
                    appKey={key}
                    actions={
                      manageable
                        ? [
                            {
                              label: t("web.settings.appKeys.row.remove"),
                              ariaLabel: t("web.settings.appKeys.row.removeLabel", {
                                device: key.name,
                              }),
                              onClick: () => open({ kind: "remove", key }),
                            },
                          ]
                        : undefined
                    }
                  />
                ))}
              </ul>
            </SettingsGroup>
          )}
          {approved.length > 0 && (
            <SettingsGroup
              title={t(`${P}.preapprovals.title`)}
              description={t(`${P}.preapprovals.description`, { username })}
              flush
            >
              <ul className="mt-3 space-y-2">
                {approved.map((preapproval) => (
                  <PreapprovalRow
                    key={preapproval.id}
                    preapproval={preapproval}
                    actions={
                      manageable
                        ? [
                            {
                              label: t("web.settings.appKeys.row.withdraw"),
                              ariaLabel: t("web.settings.appKeys.row.withdrawLabel", {
                                device: preapproval.device_name,
                              }),
                              onClick: () => open({ kind: "withdraw", preapproval }),
                            },
                          ]
                        : undefined
                    }
                  />
                ))}
              </ul>
            </SettingsGroup>
          )}
          {!manageable && <p className="text-muted-foreground text-xs">{t(`${P}.ownerOnly`)}</p>}
        </>
      )}
      <p className="text-muted-foreground text-xs leading-relaxed">{t(`${P}.footnote`)}</p>
      <ConfirmDialog
        open={selection !== null}
        onOpenChange={(next) => {
          if (!next && !pending) setSelection(null);
        }}
        title={dialog.title}
        description={dialog.description}
        confirmLabel={pending ? t(`${P}.confirm.working`) : dialog.confirm}
        variant="destructive"
        onConfirm={() => void confirm()}
        isPending={pending}
        error={error}
        className={TOUCH_TARGETS}
      />
    </section>
  );
}

function dialogText(
  t: ReturnType<typeof useT>,
  selection: Selection | null,
  username: string,
): { title: string; description: string; confirm: string } {
  switch (selection?.kind) {
    case "remove":
      return {
        title: t(`${P}.confirm.removeTitle`, { device: selection.key.name }),
        description: t(`${P}.confirm.removeDescription`, { device: selection.key.name, username }),
        confirm: t(`${P}.confirm.remove`),
      };
    case "deny":
      return {
        title: t(`${P}.confirm.denyTitle`, { device: selection.key.name }),
        description: t(`${P}.confirm.denyDescription`, { device: selection.key.name, username }),
        confirm: t(`${P}.confirm.deny`),
      };
    case "person":
      return {
        title: t(`${P}.confirm.personKeyTitle`, { username }),
        description: t(`${P}.confirm.personKeyDescription`, { username }),
        confirm: t(`${P}.confirm.personKey`),
      };
    case "withdraw":
      return {
        title: t(`${P}.confirm.withdrawTitle`, { device: selection.preapproval.device_name }),
        description: t(`${P}.confirm.withdrawDescription`, {
          device: selection.preapproval.device_name,
          username,
        }),
        confirm: t(`${P}.confirm.withdraw`),
      };
    default:
      return { title: "", description: "", confirm: "" };
  }
}
