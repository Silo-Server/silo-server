import { useEffect, useRef, useState, type ReactNode } from "react";
import { KeyRound, Loader2, RefreshCw } from "lucide-react";
import { toast } from "sonner";
import { SettingsGroup } from "@/components/settings/SettingsGroup";
import { Button } from "@/components/ui/button";
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
import { V2ProblemError, v2ErrorText } from "@/api/v2/request";
import { ProfilePinDialog } from "@/components/profiles/ProfilePinDialog";
import { SignInAgainNotice } from "@/components/appKeys/SignInAgainNotice";
import { AppKeyRow, PreapprovalRow, type DevicePreapproval } from "@/components/appKeys/AppKeyRow";
import { useScrollToDevice } from "@/components/appKeys/useScrollToDevice";
import {
  useAppKeyCapability,
  useAppKeySessionAccess,
  type AppKeySessionAccess,
} from "@/hooks/queries/appKeySession";
import { useOwnAppKeys, type OwnAppDeviceKey } from "@/hooks/queries/ownAppKeys";
import { useAuth } from "@/hooks/useAuth";
import { formatDateTime } from "@/lib/datetime";
import { TOUCH_TARGETS } from "@/lib/touchTargets";
import { useT } from "@/i18n";

type Selection =
  | { kind: "remove"; key: OwnAppDeviceKey }
  | { kind: "approve"; key: OwnAppDeviceKey }
  | { kind: "deny"; key: OwnAppDeviceKey }
  | { kind: "withdraw"; preapproval: DevicePreapproval };

type CapabilityState = "available" | "disabled" | "not_configured" | "unsupported";

const P = "web.settings.appKeys";

/** A device name inside a translated sentence keeps its own direction. */
function Name({ children }: { children: string }) {
  return <span dir="auto">{children}</span>;
}

/**
 * A person's own Silo app devices on Settings → Signed-in sessions: the
 * account key, devices waiting for approval, devices, and sign-ins approved
 * in advance. What this browser session may do comes from the server first
 * (useAppKeySessionAccess): a session that can't list devices gets
 * SignInAgainNotice instead of an error, and a profile that isn't the
 * household's main one gets the list without Approve. An administrator
 * acting as the account sees nothing here.
 */
export function OwnAppKeysPanel() {
  const t = useT();
  const { isImpersonating } = useAuth();
  const capability = useAppKeyCapability();
  const state = capability.data?.state;
  const shown = !isImpersonating && capability.isSuccess && state !== "unsupported";
  const access = useAppKeySessionAccess(shown);
  const heading = useRef<HTMLHeadingElement>(null);
  useScrollToDevice(shown && !access.isPending);
  if (!shown) return null;

  const header = (
    <div className="min-w-0 space-y-2">
      <h2
        id="own-devices-title"
        ref={heading}
        tabIndex={-1}
        className="text-lg font-semibold tracking-tight focus:outline-none"
      >
        {t(`${P}.title`)}
      </h2>
      <p className="text-muted-foreground max-w-2xl text-sm leading-relaxed">
        {t(`${P}.description`)}
      </p>
    </div>
  );
  let body: ReactNode;
  if (access.isPending) body = <Loading />;
  else if (access.isError)
    body = <LoadFailed onRetry={() => void access.refetch()} busy={access.isFetching} />;
  else if (!access.data.list) body = <SignInAgainNotice signInAgain={access.data.sign_in_again} />;
  else
    return (
      <Section>
        <DeviceLists
          header={header}
          access={access.data}
          state={state}
          heading={heading}
          onRefused={access.refetch}
        />
      </Section>
    );
  return (
    <Section>
      {header}
      {body}
    </Section>
  );
}

function Section({ children }: { children: ReactNode }) {
  return (
    <section id="devices" aria-labelledby="own-devices-title" className="scroll-mt-24 space-y-6">
      {children}
    </section>
  );
}

function Loading() {
  const t = useT();
  return (
    <p role="status" className="text-muted-foreground flex items-center gap-2 text-sm">
      <Loader2 className="size-4 animate-spin" aria-hidden="true" />
      {t(`${P}.loading`)}
    </p>
  );
}

function LoadFailed({ onRetry, busy }: { onRetry: () => void; busy: boolean }) {
  const t = useT();
  return (
    <div role="alert" className="surface-panel-raised space-y-3 p-5">
      <p>{t(`${P}.loadFailed`)}</p>
      <Button variant="outline" onClick={onRetry} disabled={busy}>
        {t("common.retry")}
      </Button>
    </div>
  );
}

function isRefusal(error: unknown) {
  return error instanceof V2ProblemError && error.problemType === "permission_denied";
}

function DeviceLists({
  header,
  access,
  state,
  heading,
  onRefused,
}: {
  header: ReactNode;
  access: AppKeySessionAccess;
  state: CapabilityState | undefined;
  heading: React.RefObject<HTMLHeadingElement | null>;
  /** Stable across renders (the access query's refetch). */
  onRefused: () => unknown;
}) {
  const t = useT();
  const { profile, selectProfile, verifyProfilePin } = useAuth();
  const { keys, preapprovals, revokeDevice, deny, approve, withdraw } = useOwnAppKeys(true);
  const [selection, setSelection] = useState<Selection | null>(null);
  const [error, setError] = useState("");
  const [askPin, setAskPin] = useState(false);
  const returnFocus = useRef<HTMLElement | null>(null);
  const pending =
    revokeDevice.isPending || deny.isPending || approve.isPending || withdraw.isPending;

  // The session changed under the page (it was replaced, say): ask again
  // what it may do, and render from that answer instead of an error.
  const refused = isRefusal(keys.error);
  useEffect(() => {
    if (refused) onRefused();
  }, [refused, onRefused]);

  const on = state === "available";
  // approve is false for a main profile whose PIN this request didn't prove;
  // the PIN dialog settles that when the person approves.
  const mainProfile = access.approve || profile?.is_primary === true;
  const canApprove = on && access.manage && mainProfile;

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
          toast.success(t(`${P}.removed`, { device: <Name>{selection.key.name}</Name> }));
          break;
        case "approve":
          await approve.mutateAsync(selection.key.id);
          toast.success(t(`${P}.approved`, { device: <Name>{selection.key.name}</Name> }));
          break;
        case "deny":
          await deny.mutateAsync(selection.key.id);
          toast.success(t(`${P}.denied`, { device: <Name>{selection.key.name}</Name> }));
          break;
        case "withdraw":
          await withdraw.mutateAsync(selection.preapproval.id);
          toast.success(t(`${P}.withdrawn`));
          break;
      }
      setSelection(null);
    } catch (err) {
      const type = err instanceof V2ProblemError ? err.problemType : "";
      if (type === "profile_verification_required") setAskPin(true);
      else if (type === "main_profile_required") setError(t(`${P}.approveMainOnly`));
      else {
        setError(v2ErrorText(err, t(`${P}.actionFailed`)));
        if (type === "permission_denied") onRefused();
      }
    }
  }

  if (keys.isPending || (keys.isError && refused))
    return (
      <>
        {header}
        <Loading />
      </>
    );
  if (keys.isError)
    return (
      <>
        {header}
        <LoadFailed onRetry={() => void keys.refetch()} busy={keys.isFetching} />
      </>
    );

  const data = keys.data;
  const waiting = data.devices.filter((key) => key.status === "pending");
  const enrolled = data.devices.filter((key) => key.status !== "pending");
  const approved = preapprovals.data ?? [];
  const empty =
    !data.person_key && data.devices.length === 0 && preapprovals.isSuccess && !approved.length;
  const dialog = dialogText(t, selection);
  return (
    <>
      <div className="flex flex-wrap items-start justify-between gap-3">
        {header}
        <Button
          size="sm"
          variant="outline"
          onClick={() => {
            void keys.refetch();
            void preapprovals.refetch();
          }}
          disabled={keys.isFetching || preapprovals.isFetching}
          aria-label={t(`${P}.refreshLabel`)}
        >
          <RefreshCw className={keys.isFetching ? "animate-spin" : undefined} aria-hidden="true" />
          {t("web.settings.sessions.refresh")}
        </Button>
      </div>
      {state && !on && (
        <p className="surface-panel-subtle text-muted-foreground p-3 text-sm">
          {state === "disabled" ? t(`${P}.off`) : t(`${P}.unavailable`)}
        </p>
      )}
      {empty ? (
        <p className="text-muted-foreground py-2 text-sm">{t(`${P}.empty`)}</p>
      ) : (
        <>
          {data.person_key && (
            <SettingsGroup title={t(`${P}.personKey.title`)} flush>
              <div className="flex flex-wrap items-center gap-3 py-2">
                <KeyRound className="text-muted-foreground size-4 shrink-0" aria-hidden="true" />
                <p className="min-w-0 flex-1 basis-60 text-sm">
                  {t(`${P}.personKey.description`, {
                    date: formatDateTime(data.person_key.enrolled_at, { seconds: false }),
                  })}
                  {data.person_key.enrolled_ip_address && (
                    <span className="text-muted-foreground block text-xs">
                      {t("web.settings.appKeys.row.enrolledFrom", {
                        address: <bdi dir="ltr">{data.person_key.enrolled_ip_address}</bdi>,
                      })}
                    </span>
                  )}
                </p>
              </div>
            </SettingsGroup>
          )}
          {!data.person_key && (
            <p className="text-muted-foreground text-sm">{t(`${P}.personKey.none`)}</p>
          )}
          {waiting.length > 0 && (
            <SettingsGroup
              title={t(`${P}.waiting.title`)}
              description={t(`${P}.waiting.description`)}
              flush
            >
              <ul className="mt-3 space-y-2">
                {waiting.map((key) => (
                  <AppKeyRow
                    key={key.id}
                    appKey={key}
                    actions={[
                      ...(canApprove
                        ? [
                            {
                              label: t(`${P}.row.approve`),
                              ariaLabel: t(`${P}.row.approveLabel`, { device: key.name }),
                              onClick: () => open({ kind: "approve", key }),
                            },
                          ]
                        : []),
                      {
                        label: t(`${P}.row.deny`),
                        ariaLabel: t(`${P}.row.denyLabel`, { device: key.name }),
                        onClick: () => open({ kind: "deny", key }),
                      },
                    ]}
                  />
                ))}
              </ul>
              {on && access.manage && !mainProfile && (
                <p className="text-muted-foreground pt-2 text-xs">{t(`${P}.approveMainOnly`)}</p>
              )}
            </SettingsGroup>
          )}
          {enrolled.length > 0 && (
            <SettingsGroup title={t(`${P}.devices.title`)} flush>
              <ul className="mt-3 space-y-2">
                {enrolled.map((key) => (
                  <AppKeyRow
                    key={key.id}
                    appKey={key}
                    actions={[
                      {
                        label: t(`${P}.row.remove`),
                        ariaLabel: t(`${P}.row.removeLabel`, { device: key.name }),
                        onClick: () => open({ kind: "remove", key }),
                      },
                    ]}
                  />
                ))}
              </ul>
            </SettingsGroup>
          )}
          {preapprovals.isError ? (
            <SettingsGroup title={t(`${P}.preapprovals.title`)} flush>
              <div role="alert" className="space-y-3 py-2 text-sm">
                <p>{t(`${P}.preapprovals.loadFailed`)}</p>
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
                title={t(`${P}.preapprovals.title`)}
                description={t(`${P}.preapprovals.description`)}
                flush
              >
                <ul className="mt-3 space-y-2">
                  {approved.map((preapproval) => (
                    <PreapprovalRow
                      key={preapproval.id}
                      preapproval={preapproval}
                      actions={[
                        {
                          label: t(`${P}.row.withdraw`),
                          ariaLabel: t(`${P}.row.withdrawLabel`, {
                            device: preapproval.device_name,
                          }),
                          onClick: () => open({ kind: "withdraw", preapproval }),
                        },
                      ]}
                    />
                  ))}
                </ul>
              </SettingsGroup>
            )
          )}
        </>
      )}
      {/* It says a signed-out device signs back in, true only while app keys are on. */}
      {on && <p className="text-muted-foreground text-xs leading-relaxed">{t(`${P}.footnote`)}</p>}
      <AlertDialog
        open={selection !== null && !askPin}
        onOpenChange={(next) => {
          if (!next && !pending) setSelection(null);
        }}
      >
        <AlertDialogContent
          className={TOUCH_TARGETS}
          onCloseAutoFocus={(event) => {
            // The row that opened the dialog is gone after most actions;
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
              variant={selection?.kind === "approve" ? "default" : "destructive"}
              disabled={pending}
              onClick={(event) => {
                event.preventDefault();
                void confirm();
              }}
            >
              {pending ? t(`${P}.confirm.working`) : dialog.confirm}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
      <ProfilePinDialog
        profile={askPin ? profile : null}
        description={t(`${P}.pinDescription`)}
        onClose={() => {
          setAskPin(false);
          setSelection(null);
        }}
        onVerified={(verified, token) => {
          setAskPin(false);
          selectProfile(verified, token);
          void confirm();
        }}
        verifyPin={verifyProfilePin}
      />
    </>
  );
}

function dialogText(
  t: ReturnType<typeof useT>,
  selection: Selection | null,
): { title: ReactNode; description: ReactNode; confirm: string } {
  if (!selection) return { title: "", description: "", confirm: "" };
  const name =
    selection.kind === "withdraw" ? selection.preapproval.device_name : selection.key.name;
  const device = <Name>{name}</Name>;
  return {
    title: t(`${P}.confirm.${selection.kind}Title`, { device }),
    description: t(`${P}.confirm.${selection.kind}Description`, { device }),
    confirm: t(`${P}.confirm.${selection.kind}`),
  };
}
