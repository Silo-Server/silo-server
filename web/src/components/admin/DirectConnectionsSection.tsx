import { Loader2 } from "lucide-react";
import type { ReactNode } from "react";

import type { components } from "@/api/v2/schema";
import { SaveBar } from "@/components/SaveBar";
import { AdvancedSection } from "@/components/settings/AdvancedSection";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import {
  useAdminDirectConnectionsStatus,
  useRefreshDirectConnections,
  type DirectConnectionsHostStatus,
} from "@/hooks/queries/admin/directConnections";
import { useSettingsForm } from "@/hooks/useSettingsForm";
import { useT, type MessageKey, type Translate } from "@/i18n";
import { joinList } from "@/lib/appLocale";
import { cn } from "@/lib/utils";
import { formatDateTime } from "@/lib/datetime";
import { formatFileSize } from "@/lib/mediaFormat";
import { FieldGroup } from "@/pages/admin-settings/FieldGroup";
import { SettingField } from "@/pages/admin-settings/SettingField";

const ENABLED = "direct_connections.enabled";
const PORT_MAPPING = "direct_connections.port_mapping";
const STUN_SERVERS = "direct_connections.stun_servers";
const ADVANCED_KEYS = [PORT_MAPPING, STUN_SERVERS];
const KEYS = [ENABLED, ...ADVANCED_KEYS];

type Feature = components["schemas"]["DirectConnectionsFeature"];
type Listener = components["schemas"]["DirectConnectionsListener"];
type DirectConnectionsKnob = components["schemas"]["DirectConnectionsKnob"];

/** A catalog key, or a name that stays as written (a protocol or product name). */
type Label = MessageKey | { name: string };

function labelText(t: Translate, label: Label) {
  return typeof label === "string" ? t(label) : label.name;
}

const FEATURE_LABELS: Record<Feature["feature"], Label> = {
  direct: "web.admin.settings.network.direct.title",
  nat_mapping: "web.admin.settings.network.direct.feature.nat_mapping",
  nat_hairpin: "web.admin.settings.network.direct.feature.nat_hairpin",
  ipv4: { name: "IPv4" },
  ipv6: { name: "IPv6" },
  port_mapping: "web.admin.settings.network.direct.portMapping.label",
  reachability: "web.admin.settings.network.direct.feature.reachability",
  socket_receive_buffer: "web.admin.settings.network.direct.feature.socket_receive_buffer",
  socket_send_buffer: "web.admin.settings.network.direct.feature.socket_send_buffer",
  gso: { name: "GSO" },
  gro: { name: "GRO" },
  pmtu_fallback: "web.admin.settings.network.direct.feature.pmtu_fallback",
  cap_net_admin: "web.admin.settings.network.direct.feature.cap_net_admin",
};

const STATE_LABELS: Record<Feature["state"], MessageKey> = {
  on: "web.admin.settings.network.direct.state.on",
  off: "web.admin.settings.network.direct.state.off",
  capped: "web.admin.settings.network.direct.state.capped",
  fallback: "web.admin.settings.network.direct.state.fallback",
  degraded: "web.admin.settings.network.direct.state.degraded",
  unknown: "web.admin.settings.network.direct.state.unknown",
};

const REASON_LABELS = {
  disabled: "web.admin.settings.network.direct.reason.disabled",
  admin_off: "web.admin.settings.network.direct.reason.admin_off",
  kernel_unsupported: "web.admin.settings.network.direct.reason.kernel_unsupported",
  limit: "web.admin.settings.network.direct.reason.limit",
  force_failed: "web.admin.settings.network.direct.reason.force_failed",
  no_cap_net_admin: "web.admin.settings.network.direct.reason.no_cap_net_admin",
  net_admin_dropped: "web.admin.settings.network.direct.reason.net_admin_dropped",
  unsupported_platform: "web.admin.settings.network.direct.reason.unsupported_platform",
  probe_failed: "web.admin.settings.network.direct.reason.probe_failed",
  not_available: "web.admin.settings.network.direct.reason.not_available",
  no_gateway: "web.admin.settings.network.direct.reason.no_gateway",
  gateway_unsupported: "web.admin.settings.network.direct.reason.gateway_unsupported",
  mapping_varies: "web.admin.settings.network.direct.reason.mapping_varies",
  not_measured: "web.admin.settings.network.direct.reason.not_measured",
  module_missing: "web.admin.settings.network.direct.reason.module_missing",
  module_not_loaded: "web.admin.settings.network.direct.reason.module_not_loaded",
  overridden: "web.admin.settings.network.direct.reason.overridden",
  kernel_error: "web.admin.settings.network.direct.reason.kernel_error",
  not_allowed: "web.admin.settings.network.direct.reason.not_allowed",
  limits_unknown: "web.admin.settings.network.direct.reason.limits_unknown",
  no_net_admin: "web.admin.settings.network.direct.reason.no_net_admin",
  helper_unavailable: "web.admin.settings.network.direct.reason.helper_unavailable",
  granted_below_request: "web.admin.settings.network.direct.reason.granted_below_request",
  not_needed: "web.admin.settings.network.direct.reason.not_needed",
  invalid: "web.admin.settings.network.direct.reason.invalid",
} satisfies Record<NonNullable<Feature["reason"]>, MessageKey> & Record<string, MessageKey>;

const PORT_MAPPING_LABELS: Record<string, string> = {
  upnp: "UPnP",
  pmp: "NAT-PMP",
  pcp: "PCP",
};

const LISTENER_LABELS: Record<Listener["listener"], Label> = {
  api: { name: "API" },
  jellyfin: { name: "Jellyfin" },
  abs: { name: "Audiobookshelf" },
  proxy: "web.admin.settings.network.direct.listener.proxy",
};

/** The label for a state or reason; a value this build does not know shows as the server sent it. */
function readableLabel(t: Translate, value: string, labels: Partial<Record<string, MessageKey>>) {
  const key = labels[value];
  return key ? t(key) : value.replaceAll("_", " ");
}

function congestionLabel(t: Translate, value: string) {
  return value === "off"
    ? t("web.admin.settings.network.direct.state.off")
    : value.toUpperCase() || t("web.admin.settings.network.direct.state.unknown");
}

const ENV_TCP_CONGESTION = "SILO_NET_TCP_CONGESTION";
const ENV_TCP_SEND_BUFFER = "SILO_NET_TCP_SEND_BUFFER";

/** The plain name of a host tuning variable; a variable with no entry shows by its name alone. */
const KNOB_LABELS: Partial<Record<string, MessageKey>> = {
  [ENV_TCP_CONGESTION]: "web.admin.settings.network.direct.knob.tcpCongestion",
  [ENV_TCP_SEND_BUFFER]: "web.admin.settings.network.direct.knob.tcpSendBuffer",
};

/**
 * A host tuning value as people read it: an algorithm name in capitals, a byte size with its
 * unit, and off as the Off state. A value the server sent that is none of these (a rejected
 * request, a variable this build does not know) shows as sent.
 */
function knobValue(t: Translate, knob: DirectConnectionsKnob, value: string) {
  if (knob.env === ENV_TCP_CONGESTION) {
    return value === "off" || knob.allowed?.includes(value) ? congestionLabel(t, value) : value;
  }
  if (knob.env === ENV_TCP_SEND_BUFFER) {
    if (value === "off") return t("web.admin.settings.network.direct.state.off");
    return /^\d+$/.test(value) ? formatFileSize(Number(value), { fallback: value }) : value;
  }
  return value;
}

/** The reason a send buffer reports when it needs no increase; the row shows the Not needed state. */
const NOT_NEEDED_REASON = "not_needed";
/** The reasons the NET_ADMIN capability row and the buffer feature rows give for a missing capability. */
const NET_ADMIN_REASONS: readonly string[] = ["no_cap_net_admin", "net_admin_dropped"];
/** A listener send buffer gives its own reason when the capability is missing. */
const LISTENER_NET_ADMIN_REASON = "no_net_admin";

/**
 * The state and reason lines a feature row shows. The NET_ADMIN capability row never repeats its
 * own name as a reason: the buffer rows that needed the capability, feature rows and listener
 * send buffers alike, say it is missing or dropped.
 */
function featureDisplay(t: Translate, feature: Feature, host: DirectConnectionsHostStatus) {
  if (
    feature.feature === "cap_net_admin" &&
    feature.state === "off" &&
    feature.reason &&
    NET_ADMIN_REASONS.includes(feature.reason)
  ) {
    const needed =
      host.features.some(
        (other) =>
          other.feature !== "cap_net_admin" &&
          other.reason &&
          NET_ADMIN_REASONS.includes(other.reason),
      ) ||
      (host.listeners ?? []).some(
        (listener) => listener.send_buffer.reason === LISTENER_NET_ADMIN_REASON,
      );
    return {
      state: needed
        ? readableLabel(t, feature.state, STATE_LABELS)
        : t("web.admin.settings.network.direct.state.notNeeded"),
      reason: undefined,
    };
  }
  return { state: readableLabel(t, feature.state, STATE_LABELS), reason: feature.reason };
}

function DiagnosticRow({
  label,
  value,
  children,
  detailsUnderValue = false,
}: {
  label: ReactNode;
  value: ReactNode;
  children: ReactNode;
  /** Keeps the detail lines directly under the value when the label runs two lines. */
  detailsUnderValue?: boolean;
}) {
  return (
    <div className="grid grid-cols-[minmax(0,1fr)_auto] items-start gap-x-6 gap-y-1 py-2">
      <dt className={cn("max-w-full min-w-0 break-words", detailsUnderValue && "row-span-2")}>
        {label}
      </dt>
      <dd className="text-end whitespace-nowrap">{value}</dd>
      <dd
        className={cn(
          "min-w-0 space-y-1 text-end break-words empty:hidden",
          detailsUnderValue ? "col-start-2" : "col-span-2",
        )}
      >
        {children}
      </dd>
    </div>
  );
}

function HostRow({ host }: { host: DirectConnectionsHostStatus }) {
  const t = useT();
  const unknown = t("web.admin.settings.network.direct.state.unknown");
  return (
    <li className="flex flex-wrap items-start justify-between gap-x-6 gap-y-3 py-4 first:pt-0 last:pb-0">
      <div className="min-w-0 flex-1 space-y-3">
        <div className="space-y-1.5">
          <h3 className="font-medium">{t("web.admin.settings.network.host.apiServer")}</h3>
          <p className="text-muted-foreground text-xs">
            {t("web.admin.settings.network.direct.lastChecked", {
              time: formatDateTime(host.checked_at, { seconds: false }),
            })}
          </p>
        </div>
        <dl className="divide-border divide-y text-sm">
          {host.features.map((feature) => {
            const display = featureDisplay(t, feature, host);
            return (
              <DiagnosticRow
                key={feature.feature}
                label={
                  feature.feature in FEATURE_LABELS
                    ? labelText(t, FEATURE_LABELS[feature.feature])
                    : feature.feature
                }
                value={display.state}
              >
                {display.reason ? (
                  <p className="text-muted-foreground text-xs">
                    {readableLabel(t, display.reason, REASON_LABELS)}
                  </p>
                ) : null}
                {feature.requested !== undefined || feature.granted !== undefined ? (
                  <p className="text-muted-foreground text-xs">
                    {t("web.admin.settings.network.direct.requestedGranted", {
                      requested: formatFileSize(Number(feature.requested), { fallback: unknown }),
                      granted: formatFileSize(Number(feature.granted), { fallback: unknown }),
                    })}
                  </p>
                ) : null}
                {feature.detail ? (
                  <p className="text-muted-foreground text-xs">
                    {feature.feature === "reachability" || feature.feature === "nat_mapping"
                      ? t("web.admin.settings.network.direct.observedAddress", {
                          address: feature.detail,
                        })
                      : feature.feature === "port_mapping"
                        ? joinList(
                            feature.detail
                              .split(",")
                              .map(
                                (protocol) =>
                                  PORT_MAPPING_LABELS[protocol] ?? protocol.toUpperCase(),
                              ),
                          )
                        : feature.detail}
                  </p>
                ) : null}
              </DiagnosticRow>
            );
          })}
        </dl>
        {host.listeners?.length ? (
          <div className="space-y-2">
            <h4 className="font-medium">{t("web.admin.settings.network.direct.listeners")}</h4>
            <dl className="divide-border divide-y text-sm">
              {host.listeners.map((listener) => (
                <DiagnosticRow
                  key={listener.listener}
                  label={
                    listener.listener in LISTENER_LABELS
                      ? labelText(t, LISTENER_LABELS[listener.listener])
                      : listener.listener
                  }
                  value={t("web.admin.settings.network.direct.congestionControl", {
                    state:
                      listener.requested === "off"
                        ? t("web.admin.settings.network.direct.state.off")
                        : readableLabel(t, listener.state, STATE_LABELS),
                  })}
                >
                  {listener.requested !== "off" ? (
                    <p className="text-muted-foreground text-xs">
                      {t("web.admin.settings.network.direct.algorithm", {
                        algorithm:
                          listener.requested === listener.granted
                            ? congestionLabel(t, listener.granted)
                            : t("web.admin.settings.network.direct.algorithmRequestedActive", {
                                requested: congestionLabel(t, listener.requested),
                                active: congestionLabel(t, listener.granted),
                              }),
                      })}
                    </p>
                  ) : null}
                  {listener.reason ? (
                    <p className="text-muted-foreground text-xs">
                      {listener.errno
                        ? t("web.admin.settings.network.direct.reasonWithErrno", {
                            reason: readableLabel(t, listener.reason, REASON_LABELS),
                            errno: listener.errno,
                          })
                        : readableLabel(t, listener.reason, REASON_LABELS)}
                    </p>
                  ) : null}
                  <p className="text-muted-foreground text-xs">
                    {t("web.admin.settings.network.direct.sendBuffer", {
                      state:
                        listener.send_buffer.reason === NOT_NEEDED_REASON
                          ? t("web.admin.settings.network.direct.state.notNeeded")
                          : readableLabel(t, listener.send_buffer.state, STATE_LABELS),
                    })}
                  </p>
                  {listener.send_buffer.requested > 0 ? (
                    <p className="text-muted-foreground text-xs">
                      {t("web.admin.settings.network.direct.requestedGranted", {
                        requested: formatFileSize(listener.send_buffer.requested),
                        granted: formatFileSize(listener.send_buffer.granted, {
                          fallback: unknown,
                        }),
                      })}
                    </p>
                  ) : null}
                  {listener.send_buffer.reason &&
                  listener.send_buffer.reason !== NOT_NEEDED_REASON ? (
                    <p className="text-muted-foreground text-xs">
                      {listener.send_buffer.errno
                        ? t("web.admin.settings.network.direct.reasonWithErrno", {
                            reason: readableLabel(t, listener.send_buffer.reason, REASON_LABELS),
                            errno: listener.send_buffer.errno,
                          })
                        : readableLabel(t, listener.send_buffer.reason, REASON_LABELS)}
                    </p>
                  ) : null}
                </DiagnosticRow>
              ))}
            </dl>
          </div>
        ) : null}
        <div className="space-y-2">
          <h4 className="font-medium">{t("web.admin.settings.network.direct.hostTuning")}</h4>
          <dl className="divide-border divide-y text-sm">
            {host.knobs.map((knob) => {
              const label = KNOB_LABELS[knob.env];
              return (
                <DiagnosticRow
                  key={knob.env}
                  detailsUnderValue
                  label={
                    label ? (
                      <>
                        <span className="block">{t(label)}</span>
                        <span className="text-muted-foreground block font-mono text-xs break-all">
                          {knob.env}
                        </span>
                      </>
                    ) : (
                      <span className="font-mono text-xs break-all">{knob.env}</span>
                    )
                  }
                  value={t("web.admin.settings.network.direct.effective", {
                    value: knobValue(t, knob, knob.effective),
                  })}
                >
                  <p className="text-muted-foreground text-xs">
                    {t("web.admin.settings.network.direct.defaultValue", {
                      value: knobValue(t, knob, knob.default),
                    })}
                  </p>
                  {knob.requested !== undefined ? (
                    <p className="text-muted-foreground text-xs">
                      {t("web.admin.settings.network.direct.requested", {
                        value: knobValue(t, knob, knob.requested),
                      })}
                    </p>
                  ) : null}
                  {knob.allowed?.length ? (
                    <p className="text-muted-foreground text-xs">
                      {t("web.admin.settings.network.direct.allowed", {
                        values: joinList(knob.allowed.map((value) => knobValue(t, knob, value))),
                      })}
                    </p>
                  ) : null}
                  {knob.reason ? (
                    <p className="text-muted-foreground text-xs">
                      {readableLabel(t, knob.reason, REASON_LABELS)}
                    </p>
                  ) : null}
                </DiagnosticRow>
              );
            })}
          </dl>
        </div>
      </div>
    </li>
  );
}

function Diagnostics() {
  const t = useT();
  const status = useAdminDirectConnectionsStatus();
  const refresh = useRefreshDirectConnections();

  return (
    <div className="space-y-4 py-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <p className="text-muted-foreground text-sm">
          {t("web.admin.settings.network.direct.readinessNote")}
        </p>
        <Button
          type="button"
          variant="outline"
          size="sm"
          disabled={refresh.isPending}
          onClick={() => refresh.mutate()}
        >
          {refresh.isPending ? (
            <Loader2 className="size-4 animate-spin" aria-hidden="true" />
          ) : null}
          {t("web.admin.settings.network.direct.runCheck")}
        </Button>
      </div>
      {refresh.isError ? (
        <p className="text-destructive text-sm" role="alert">
          {refresh.error.message}
        </p>
      ) : null}
      {status.isLoading ? (
        <div className="space-y-3" aria-busy="true">
          <Skeleton className="h-5 w-2/3" />
          <Skeleton className="h-20 w-full" />
        </div>
      ) : status.isError ? (
        <p className="text-destructive text-sm" role="alert">
          {status.error.message}
        </p>
      ) : (
        <ul className="divide-border divide-y">
          {status.data?.hosts.map((host) => (
            <HostRow key={host.host.id} host={host} />
          ))}
        </ul>
      )}
    </div>
  );
}

/** Readiness controls shown after an administrator enables the hidden setting through the API. */
export function DirectConnectionsSection() {
  const t = useT();
  const form = useSettingsForm({ keys: KEYS });

  // Unsaved edits keep the controls and SaveBar reachable, so an administrator can still
  // save or discard a draft after another administrator turns the feature off.
  if (form.getPersistedValue(ENABLED) !== "true" && form.dirtyCount === 0) return null;

  return (
    <>
      <FieldGroup
        label={t("web.admin.settings.network.direct.title")}
        description={t("web.admin.settings.network.direct.description")}
        dirty={form.dirtyCount > 0}
      >
        <SettingField
          label={t("web.admin.settings.network.direct.enable")}
          type="toggle"
          value={form.getValue(ENABLED)}
          onChange={(value) => form.setValue(ENABLED, value)}
          dirty={form.isDirty(ENABLED)}
          disabled={form.isSaving}
        />
        <Diagnostics />
        <AdvancedSection
          id="network-access.direct-connections"
          count={ADVANCED_KEYS.length}
          forceOpen={ADVANCED_KEYS.some((key) => form.isDirty(key))}
        >
          <SettingField
            label={t("web.admin.settings.network.direct.portMapping.label")}
            type="toggle"
            description={t("web.admin.settings.network.direct.portMapping.description")}
            value={form.getValue(PORT_MAPPING)}
            onChange={(value) => form.setValue(PORT_MAPPING, value)}
            dirty={form.isDirty(PORT_MAPPING)}
            disabled={form.isSaving}
          />
          <SettingField
            label={t("web.admin.settings.network.direct.stunServers.label")}
            description={t("web.admin.settings.network.direct.stunServers.description")}
            value={form.getValue(STUN_SERVERS)}
            onChange={(value) => form.setValue(STUN_SERVERS, value)}
            dirty={form.isDirty(STUN_SERVERS)}
            disabled={form.isSaving}
          />
        </AdvancedSection>
      </FieldGroup>
      <SaveBar
        dirtyCount={form.dirtyCount}
        onSave={form.save}
        onDiscard={form.discard}
        isSaving={form.isSaving}
      />
    </>
  );
}
