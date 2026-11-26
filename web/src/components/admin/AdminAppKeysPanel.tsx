import { useRef, useState, type ReactNode } from "react";
import { KeyRound, Loader2, RefreshCw } from "lucide-react";
import { toast } from "sonner";
import { SettingsGroup } from "@/components/settings/SettingsGroup";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { v2ErrorText } from "@/api/v2/request";
import { useAdminAppKeys, type AdminAppDeviceKey } from "@/hooks/queries/admin/appKeys";
import { useAppKeyCapability } from "@/hooks/queries/appKeys";
import { AppKeyRow, PreapprovalRow, type DevicePreapproval } from "@/components/appKeys/AppKeyRow";
import { useScrollToDevice } from "@/components/appKeys/useScrollToDevice";
import { formatDateTime } from "@/lib/datetime";
import { TOUCH_TARGETS } from "@/lib/touchTargets";
import { useT } from "@/i18n";

type Selection =
  | { kind: "remove"; key: AdminAppDeviceKey }
  | { kind: "deny"; key: AdminAppDeviceKey }
  | { kind: "person" }
  | { kind: "withdraw"; preapproval: DevicePreapproval };

const P = "web.admin.users.detail.appKeys";
/** Text both devices panels say the same way. */
const S = "web.settings.appKeys";

/** A user or device name inside a translated sentence keeps its own direction. */
function Name({ children }: { children: string }) {
  return <span dir="auto">{children}</span>;
}

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
  user: { id: number; username: string; enabled?: boolean };
  manageable: boolean;
}) {
  const t = useT();
  const { keys, preapprovals, revokeDevice, revokePersonKey, deny, withdraw } = useAdminAppKeys(
    user.id,
  );
  const capability = useAppKeyCapability().data?.state;
  // Links from signing out, a password reset or a session row land here.
  useScrollToDevice(!keys.isPending);
  const [selection, setSelection] = useState<Selection | null>(null);
  const [error, setError] = useState("");
  const heading = useRef<HTMLHeadingElement>(null);
  const returnFocus = useRef<HTMLElement | null>(null);
  const pending =
    revokeDevice.isPending || revokePersonKey.isPending || deny.isPending || withdraw.isPending;
  const username = <Name>{user.username}</Name>;

  function open(next: Selection) {
    returnFocus.current =
      document.activeElement instanceof HTMLElement ? document.activeElement : null;
    setError("");
    setSelection(next);
  }

  async function confirm() {
    if (!selection || pending) return;
    setError("");
    try {
      switch (selection.kind) {
        case "remove":
          await revokeDevice.mutateAsync(selection.key.id);
          toast.success(t(`${S}.removed`, { device: <Name>{selection.key.name}</Name> }));
          break;
        case "deny":
          await deny.mutateAsync(selection.key.id);
          toast.success(t(`${S}.denied`, { device: <Name>{selection.key.name}</Name> }));
          break;
        case "person":
          await revokePersonKey.mutateAsync();
          toast.success(t(`${P}.personKeyRemoved`));
          break;
        case "withdraw":
          await withdraw.mutateAsync(selection.preapproval.id);
          toast.success(t(`${S}.withdrawn`));
          break;
      }
      setSelection(null);
    } catch (err) {
      setError(v2ErrorText(err, t(`${S}.actionFailed`)));
    }
  }

  const data = keys.data;
  const devices = data?.devices ?? [];
  const waiting = devices.filter((key) => key.status === "pending" && !key.identity_gone);
  const enrolled = devices.filter((key) => key.status !== "pending" || key.identity_gone);
  const approved = preapprovals.data ?? [];
  const empty =
    data && !data.person_key && devices.length === 0 && preapprovals.isSuccess && !approved.length;
  const dialog = dialogText(t, selection, username);
  return (
    <section id="devices" className="scroll-mt-24 space-y-6">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0 space-y-2">
          <h2
            ref={heading}
            tabIndex={-1}
            className="text-lg font-semibold tracking-tight focus:outline-none"
          >
            {t(`${S}.title`)}
          </h2>
          <p className="text-muted-foreground max-w-2xl text-sm leading-relaxed">
            {t(`${P}.description`, { username })}
          </p>
        </div>
        <Button
          size="sm"
          variant="outline"
          onClick={() => {
            void keys.refetch();
            void preapprovals.refetch();
          }}
          disabled={keys.isFetching || preapprovals.isFetching}
          aria-label={t(`${S}.refreshLabel`)}
        >
          <RefreshCw className={keys.isFetching ? "animate-spin" : undefined} aria-hidden="true" />
          {t("web.settings.sessions.refresh")}
        </Button>
      </div>
      {capability && capability !== "available" && (
        <p className="surface-panel-subtle text-muted-foreground p-3 text-sm">
          {capability === "disabled" ? t(`${P}.off`) : t(`${S}.unavailable`)}
        </p>
      )}
      {keys.isPending ? (
        <p role="status" className="text-muted-foreground flex items-center gap-2 text-sm">
          <Loader2 className="size-4 animate-spin" aria-hidden="true" />
          {t(`${S}.loading`)}
        </p>
      ) : keys.isError ? (
        <div role="alert" className="surface-panel-raised space-y-3 p-5">
          <p>{t(`${P}.loadFailed`)}</p>
          <Button variant="outline" onClick={() => void keys.refetch()} disabled={keys.isFetching}>
            {t("common.retry")}
          </Button>
        </div>
      ) : empty ? (
        <p className="text-muted-foreground py-2 text-sm">{t(`${P}.empty`)}</p>
      ) : (
        <>
          <SettingsGroup title={t(`${S}.personKey.title`)} flush>
            <div className="flex flex-wrap items-center gap-3 py-2">
              <KeyRound className="text-muted-foreground size-4 shrink-0" aria-hidden="true" />
              <p className="min-w-0 flex-1 basis-60 text-sm">
                {data?.person_key ? (
                  <>
                    {t(`${P}.personKey.description`, {
                      username,
                      date: formatDateTime(data.person_key.enrolled_at, { seconds: false }),
                    })}
                    {data.person_key.enrolled_ip_address && (
                      <span className="text-muted-foreground block text-xs">
                        {t("web.settings.appKeys.row.enrolledFrom", {
                          address: <bdi dir="ltr">{data.person_key.enrolled_ip_address}</bdi>,
                        })}
                      </span>
                    )}
                  </>
                ) : (
                  t(`${P}.personKey.none`, { username })
                )}
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
            <SettingsGroup title={t(`${S}.devices.title`)} flush>
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
          {preapprovals.isError ? (
            <SettingsGroup title={t(`${S}.preapprovals.title`)} flush>
              <div role="alert" className="space-y-3 py-2 text-sm">
                <p>{t(`${S}.preapprovals.loadFailed`)}</p>
                <Button
                  size="sm"
                  variant="outline"
                  onClick={() => void preapprovals.refetch()}
                  disabled={preapprovals.isFetching}
                >
                  {t("common.retry")}
                </Button>
              </div>
            </SettingsGroup>
          ) : (
            approved.length > 0 && (
              <SettingsGroup
                title={t(`${S}.preapprovals.title`)}
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
            )
          )}
          {!manageable && <p className="text-muted-foreground text-xs">{t(`${P}.ownerOnly`)}</p>}
        </>
      )}
      {/* It says a signed-out device signs back in: not while app key sign-in
          is off, nor for a disabled account, whose keys are refused. */}
      {capability === "available" && user.enabled !== false && (
        <p className="text-muted-foreground text-xs leading-relaxed">{t(`${S}.footnote`)}</p>
      )}
      <AlertDialog
        open={selection !== null}
        onOpenChange={(next) => {
          if (!next && !pending) setSelection(null);
        }}
      >
        <AlertDialogContent
          className={TOUCH_TARGETS}
          onCloseAutoFocus={(event) => {
            // The row that opened the dialog is gone after Remove or Deny;
            // focus then goes to the section heading.
            event.preventDefault();
            const target = returnFocus.current?.isConnected ? returnFocus.current : heading.current;
            target?.focus();
          }}
        >
          <AlertDialogHeader>
            <AlertDialogTitle>{dialog.title}</AlertDialogTitle>
            <AlertDialogDescription>{dialog.description}</AlertDialogDescription>
          </AlertDialogHeader>
          {error && (
            <p role="alert" className="text-destructive text-sm">
              {error}
            </p>
          )}
          <AlertDialogFooter>
            <AlertDialogCancel disabled={pending}>{t("common.cancel")}</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              disabled={pending}
              onClick={(event) => {
                event.preventDefault();
                void confirm();
              }}
            >
              {pending ? t(`${S}.confirm.working`) : dialog.confirm}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </section>
  );
}

function dialogText(
  t: ReturnType<typeof useT>,
  selection: Selection | null,
  username: ReactNode,
): { title: ReactNode; description: ReactNode; confirm: string } {
  switch (selection?.kind) {
    case "remove": {
      const device = <Name>{selection.key.name}</Name>;
      return {
        title: t(`${S}.confirm.removeTitle`, { device }),
        description: t(`${P}.confirm.removeDescription`, { device, username }),
        confirm: t(`${S}.confirm.remove`),
      };
    }
    case "deny": {
      const device = <Name>{selection.key.name}</Name>;
      return {
        title: t(`${S}.confirm.denyTitle`, { device }),
        description: t(`${P}.confirm.denyDescription`, { device, username }),
        confirm: t(`${S}.confirm.deny`),
      };
    }
    case "person":
      return {
        title: t(`${P}.confirm.personKeyTitle`, { username }),
        description: t(`${P}.confirm.personKeyDescription`, { username }),
        confirm: t(`${P}.confirm.personKey`),
      };
    case "withdraw": {
      const device = <Name>{selection.preapproval.device_name}</Name>;
      return {
        title: t(`${S}.confirm.withdrawTitle`, { device }),
        description: t(`${P}.confirm.withdrawDescription`, { device, username }),
        confirm: t(`${S}.confirm.withdraw`),
      };
    }
    default:
      return { title: "", description: "", confirm: "" };
  }
}
