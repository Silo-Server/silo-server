import type { ReactNode } from "react";
import { HelpCircle, Monitor, Smartphone, Tv } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { formatDateTime } from "@/lib/datetime";
import { loginSessionDevice } from "@/lib/loginSessionDevice";
import { useT } from "@/i18n";
import type { AppKeyRowKey, DevicePreapproval } from "@/hooks/queries/appKeys";

/** One button on a device row: its visible label and its accessible name. */
export type AppKeyRowAction = { label: string; ariaLabel: string; onClick: () => void };

const ICONS = { tv: Tv, phone: Smartphone, desktop: Monitor, unknown: HelpCircle } as const;

/**
 * The frame every row of the Silo app devices list shares: icon, name,
 * badges, detail lines and the row's buttons.
 */
function DeviceRow({
  name,
  platform,
  badges,
  lines,
  actions = [],
}: {
  name: string;
  platform: string;
  badges?: ReactNode;
  lines: ReactNode[];
  actions?: AppKeyRowAction[];
}) {
  const device = loginSessionDevice({ device_name: name, device_platform: platform });
  const Icon = ICONS[device.kind];
  return (
    <li className="surface-panel-subtle px-3 py-3 sm:px-4">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
        <span className="border-border text-muted-foreground flex size-9 shrink-0 items-center justify-center rounded-xl border">
          <Icon className="size-4" aria-hidden="true" />
        </span>
        <div className="min-w-0 flex-1 basis-40">
          <div className="flex flex-wrap items-center gap-2 text-sm font-medium">
            <span className="max-w-full min-w-0 wrap-anywhere">{device.name}</span>
            {badges}
          </div>
          {lines.map((line, index) => (
            <p key={index} className="text-muted-foreground mt-1 text-xs wrap-anywhere">
              {line}
            </p>
          ))}
        </div>
        {actions.length > 0 && (
          <div className="ms-auto flex items-center gap-1">
            {actions.map((action) => (
              <Button
                key={action.label}
                type="button"
                size="sm"
                variant="outline"
                aria-label={action.ariaLabel}
                onClick={action.onClick}
              >
                {action.label}
              </Button>
            ))}
          </div>
        )}
      </div>
    </li>
  );
}

/**
 * One device or app key of an account, in an administrator's list or the
 * person's own: what it is, how it was added, when it last signed in, and
 * whether it can still sign in. The caller passes the buttons it allows.
 */
export function AppKeyRow({
  appKey,
  actions,
}: {
  appKey: AppKeyRowKey;
  actions?: AppKeyRowAction[];
}) {
  const t = useT();
  const how =
    appKey.enrolled_by === "grant"
      ? t("web.settings.appKeys.row.allowedBy", { name: appKey.allowed_by ?? "" })
      : appKey.enrolled_by === "introduce"
        ? t("web.settings.appKeys.row.viaPersonKey")
        : appKey.enrolled_by === "app-code"
          ? t("web.settings.appKeys.row.viaAppCode")
          : t("web.settings.appKeys.row.viaSession");
  const waiting = appKey.status === "pending";
  const lines: ReactNode[] = [
    [
      how,
      t("web.settings.appKeys.row.added", {
        date: formatDateTime(appKey.enrolled_at, { seconds: false }),
      }),
    ].join(" · "),
  ];
  if (appKey.identity_gone) {
    lines.push(
      <span className="text-destructive">{t("web.settings.appKeys.row.identityGone")}</span>,
    );
  } else if (waiting && appKey.waiting_until) {
    lines.push(
      t("web.settings.appKeys.row.waitingUntil", {
        time: formatDateTime(appKey.waiting_until, { seconds: false }),
      }),
    );
  } else if (!waiting) {
    lines.push(
      appKey.last_used_at
        ? t("web.settings.appKeys.row.lastUsed", {
            time: formatDateTime(appKey.last_used_at, { seconds: false }),
          })
        : t("web.settings.appKeys.row.notUsed"),
    );
  }
  return (
    <DeviceRow
      name={appKey.name}
      platform={appKey.platform}
      badges={
        <>
          <Badge variant="outline">
            {appKey.kind === "app"
              ? t("web.settings.appKeys.row.kindApp")
              : t("web.settings.appKeys.row.kindDevice")}
          </Badge>
          {appKey.provider_bound && !appKey.identity_gone && (
            <Badge variant="outline">{t("web.settings.appKeys.row.provider")}</Badge>
          )}
        </>
      }
      lines={lines}
      actions={actions}
    />
  );
}

/** A device sign-in approved in advance that the device hasn't redeemed yet. */
export function PreapprovalRow({
  preapproval,
  actions,
}: {
  preapproval: DevicePreapproval;
  actions?: AppKeyRowAction[];
}) {
  const t = useT();
  return (
    <DeviceRow
      name={preapproval.device_name}
      platform={preapproval.device_platform}
      lines={[
        t("web.settings.appKeys.row.preapproval", {
          date: formatDateTime(preapproval.expires_at, { seconds: false }),
        }),
      ]}
      actions={actions}
    />
  );
}
