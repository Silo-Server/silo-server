import { useMemo, useState } from "react";
import { AlertTriangle } from "lucide-react";
import { useRateLimitConfig, useUpdateRateLimitConfig } from "@/hooks/queries/admin/rateLimits";
import type {
  RateLimitConfig,
  RateLimitTierConfig,
  RateLimitAuthEndpointConfig,
} from "@/api/types";
import { Switch } from "@/components/ui/switch";
import { Label } from "@/components/ui/label";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { RestartServerButton } from "./RestartServerButton";

const DEFAULT_TIER: RateLimitTierConfig = {
  requests_per_second: 10,
  requests_per_minute: 300,
  burst: 20,
};

const DEFAULT_AUTH_ENDPOINT: RateLimitAuthEndpointConfig = {
  requests_per_minute: 20,
  burst: 10,
};

const DEFAULT_CONFIG: RateLimitConfig = {
  enabled: true,
  backend: "memory",
  global_requests_per_second: 1000,
  tiers: {
    standard: { requests_per_second: 20, requests_per_minute: 1200, burst: 20 },
    elevated: { requests_per_second: 100, requests_per_minute: 6000, burst: 100 },
  },
  ip_requests_per_second: 120,
  ip_requests_per_minute: 6000,
  ip_burst: 120,
  auth_endpoints: {
    login: { requests_per_minute: 20, burst: 10 },
    signup: { requests_per_minute: 10, burst: 6 },
    setup: { requests_per_minute: 10, burst: 6 },
    device_start: { requests_per_minute: 20, burst: 10 },
    device_lookup: { requests_per_minute: 60, burst: 20 },
    device_poll: { requests_per_minute: 120, burst: 30 },
    autoscan_webhook: { requests_per_minute: 60, burst: 30 },
  },
};

const TIER_LABELS: Record<string, string> = {
  standard: "Standard",
  elevated: "Elevated",
};

const AUTH_ENDPOINT_LABELS: Record<string, string> = {
  login: "Login",
  signup: "Signup",
  setup: "Setup",
  device_start: "Device Authorization Start",
  device_lookup: "Device Authorization Lookup",
  device_poll: "Device Authorization Polling",
  autoscan_webhook: "Autoscan Webhook",
};

export default function RateLimitSettings() {
  const { data: serverConfig, isLoading } = useRateLimitConfig();
  const updateConfig = useUpdateRateLimitConfig();
  const hydratedConfig = useMemo<RateLimitConfig>(() => {
    if (!serverConfig) return DEFAULT_CONFIG;
    return {
      enabled: serverConfig.enabled,
      backend: serverConfig.backend || "memory",
      global_requests_per_second: serverConfig.global_requests_per_second,
      tiers: {
        standard: serverConfig.tiers?.standard ?? DEFAULT_CONFIG.tiers.standard!,
        elevated: serverConfig.tiers?.elevated ?? DEFAULT_CONFIG.tiers.elevated!,
      },
      ip_requests_per_second:
        serverConfig.ip_requests_per_second ?? DEFAULT_CONFIG.ip_requests_per_second,
      ip_requests_per_minute:
        serverConfig.ip_requests_per_minute ?? DEFAULT_CONFIG.ip_requests_per_minute,
      ip_burst: serverConfig.ip_burst ?? DEFAULT_CONFIG.ip_burst,
      auth_endpoints: Object.fromEntries(
        Object.keys(AUTH_ENDPOINT_LABELS).map((endpoint) => [
          endpoint,
          serverConfig.auth_endpoints?.[endpoint] ??
            DEFAULT_CONFIG.auth_endpoints[endpoint] ??
            DEFAULT_AUTH_ENDPOINT,
        ]),
      ),
    };
  }, [serverConfig]);
  const hydratedKey = JSON.stringify(hydratedConfig);
  const [configState, setConfigState] = useState<{ key: string; config: RateLimitConfig }>({
    key: hydratedKey,
    config: hydratedConfig,
  });
  const config = configState.key === hydratedKey ? configState.config : hydratedConfig;

  function updateConfigState(updater: (prev: RateLimitConfig) => RateLimitConfig) {
    setConfigState((prev) => {
      const base = prev.key === hydratedKey ? prev.config : hydratedConfig;
      return {
        key: hydratedKey,
        config: updater(base),
      };
    });
  }

  function handleTierChange(tier: string, field: keyof RateLimitTierConfig, value: string) {
    const num = parseInt(value, 10);
    if (isNaN(num) || num <= 0) return;
    updateConfigState((prev) => {
      const existing: RateLimitTierConfig = prev.tiers[tier] ?? DEFAULT_TIER;
      return {
        ...prev,
        tiers: {
          ...prev.tiers,
          [tier]: {
            ...existing,
            [field]: num,
          },
        },
      };
    });
  }

  function handleAuthEndpointChange(
    endpoint: string,
    field: keyof RateLimitAuthEndpointConfig,
    value: string,
  ) {
    const num = parseInt(value, 10);
    if (isNaN(num) || num <= 0) return;
    updateConfigState((prev) => {
      const existing: RateLimitAuthEndpointConfig =
        prev.auth_endpoints[endpoint] ?? DEFAULT_AUTH_ENDPOINT;
      return {
        ...prev,
        auth_endpoints: {
          ...prev.auth_endpoints,
          [endpoint]: {
            ...existing,
            [field]: num,
          },
        },
      };
    });
  }

  function handleSave() {
    updateConfig.mutate(config);
  }

  // The limiter only starts (and only switches backend) at boot, so the saved
  // config can disagree with what this process is actually enforcing.
  const pendingRestart =
    !!serverConfig &&
    ((serverConfig.enabled && serverConfig.active === false) ||
      (serverConfig.active === true &&
        !!serverConfig.active_backend &&
        serverConfig.backend !== serverConfig.active_backend));

  if (isLoading) return <div>Loading...</div>;

  return (
    <div className="flex flex-col gap-4">
      <div className="mb-6 space-y-2">
        <h2 className="text-xl font-semibold tracking-tight">Rate Limiting</h2>
        <p className="text-muted-foreground text-sm leading-relaxed">
          Configure request budgets for protected API routes, API keys, and public authentication or
          Autoscan endpoints.
        </p>
      </div>

      <fieldset disabled={updateConfig.isPending} className="max-w-2xl space-y-4">
        {pendingRestart && (
          <div className="surface-panel-subtle flex flex-col gap-3 rounded-xl p-4 sm:flex-row sm:items-center sm:justify-between">
            <div className="text-foreground/80 flex items-center gap-2 text-xs">
              <AlertTriangle className="h-3.5 w-3.5 flex-shrink-0" />
              <span>Saved changes require a server restart to take effect.</span>
            </div>
            <RestartServerButton />
          </div>
        )}
        <div className="surface-panel rounded-2xl border-0 px-5 py-4">
          <div className="flex flex-col justify-between gap-3 sm:flex-row sm:items-center">
            <div className="space-y-0.5">
              <Label htmlFor="rate-limit-enabled" className="text-sm font-medium">
                Enable Rate Limiting
              </Label>
              <p className="text-muted-foreground text-xs">
                When disabled, no rate limits are enforced.
              </p>
            </div>
            <Switch
              id="rate-limit-enabled"
              checked={config.enabled}
              onCheckedChange={(checked) =>
                updateConfigState((prev) => ({ ...prev, enabled: checked }))
              }
            />
          </div>
        </div>

        <div className="surface-panel rounded-2xl border-0 px-5 py-4">
          <div className="space-y-1">
            <Label htmlFor="backend" className="text-sm font-medium">
              Backend
            </Label>
            <Select
              value={config.backend}
              onValueChange={(value) => updateConfigState((prev) => ({ ...prev, backend: value }))}
            >
              <SelectTrigger id="backend" className="w-full sm:w-40">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="memory">In-Memory</SelectItem>
                <SelectItem value="redis">Redis</SelectItem>
              </SelectContent>
            </Select>
            <p className="text-muted-foreground text-xs">
              Backend changes require a restart. Redis is recommended for multi-instance deployments
              and must first be configured under Database.
            </p>
          </div>
        </div>

        <div className="surface-panel rounded-2xl border-0 px-5 py-4">
          <div className="mb-3 text-sm font-semibold">Global Settings</div>
          <div className="space-y-1">
            <Label htmlFor="global-rps" className="text-sm font-medium">
              Global Requests Per Second
            </Label>
            <Input
              id="global-rps"
              type="number"
              min={1}
              value={config.global_requests_per_second}
              onChange={(e) => {
                const num = parseInt(e.target.value, 10);
                if (!isNaN(num) && num > 0) {
                  updateConfigState((prev) => ({ ...prev, global_requests_per_second: num }));
                }
              }}
              className="w-full sm:w-40"
            />
            <p className="text-muted-foreground text-xs">
              Maximum requests per second across every route protected by the rate limiter.
            </p>
          </div>
        </div>

        <div className="surface-panel rounded-2xl border-0 px-5 py-4">
          <div className="mb-1 text-sm font-semibold">Per-IP Limits</div>
          <p className="text-muted-foreground mb-3 text-xs">
            Shared across protected authenticated routes and the public auth/Autoscan endpoints for
            one IP address.
          </p>
          <div className="grid gap-4 sm:grid-cols-3">
            <div className="space-y-1">
              <Label htmlFor="ip-rps" className="text-sm font-medium">
                Requests / Second
              </Label>
              <Input
                id="ip-rps"
                type="number"
                min={1}
                value={config.ip_requests_per_second}
                onChange={(e) => {
                  const num = parseInt(e.target.value, 10);
                  if (!isNaN(num) && num > 0) {
                    updateConfigState((prev) => ({ ...prev, ip_requests_per_second: num }));
                  }
                }}
                className="w-full"
              />
            </div>
            <div className="space-y-1">
              <Label htmlFor="ip-rpm" className="text-sm font-medium">
                Requests / Minute
              </Label>
              <Input
                id="ip-rpm"
                type="number"
                min={1}
                value={config.ip_requests_per_minute}
                onChange={(e) => {
                  const num = parseInt(e.target.value, 10);
                  if (!isNaN(num) && num > 0) {
                    updateConfigState((prev) => ({ ...prev, ip_requests_per_minute: num }));
                  }
                }}
                className="w-full"
              />
            </div>
            <div className="space-y-1">
              <Label htmlFor="ip-burst" className="text-sm font-medium">
                Burst
              </Label>
              <Input
                id="ip-burst"
                type="number"
                min={1}
                value={config.ip_burst}
                onChange={(e) => {
                  const num = parseInt(e.target.value, 10);
                  if (!isNaN(num) && num > 0) {
                    updateConfigState((prev) => ({ ...prev, ip_burst: num }));
                  }
                }}
                className="w-full"
              />
            </div>
          </div>
        </div>

        {/* Tier Settings */}
        {Object.keys(TIER_LABELS).map((tier) => {
          const tierConfig = config.tiers[tier] ?? DEFAULT_TIER;
          return (
            <div key={tier} className="surface-panel rounded-2xl border-0 px-5 py-4">
              <div className="mb-1 text-sm font-semibold">{TIER_LABELS[tier]} Tier</div>
              <p className="text-muted-foreground mb-3 text-xs">
                Per API key limits for the {TIER_LABELS[tier]!.toLowerCase()} tier.
              </p>
              <div className="grid gap-4 sm:grid-cols-3">
                <div className="space-y-1">
                  <Label htmlFor={`${tier}-rps`} className="text-sm font-medium">
                    Requests / Second
                  </Label>
                  <Input
                    id={`${tier}-rps`}
                    type="number"
                    min={1}
                    value={tierConfig.requests_per_second}
                    onChange={(e) => handleTierChange(tier, "requests_per_second", e.target.value)}
                    className="w-full"
                  />
                </div>
                <div className="space-y-1">
                  <Label htmlFor={`${tier}-rpm`} className="text-sm font-medium">
                    Requests / Minute
                  </Label>
                  <Input
                    id={`${tier}-rpm`}
                    type="number"
                    min={1}
                    value={tierConfig.requests_per_minute}
                    onChange={(e) => handleTierChange(tier, "requests_per_minute", e.target.value)}
                    className="w-full"
                  />
                </div>
                <div className="space-y-1">
                  <Label htmlFor={`${tier}-burst`} className="text-sm font-medium">
                    Burst
                  </Label>
                  <Input
                    id={`${tier}-burst`}
                    type="number"
                    min={1}
                    value={tierConfig.burst}
                    onChange={(e) => handleTierChange(tier, "burst", e.target.value)}
                    className="w-full"
                  />
                </div>
              </div>
            </div>
          );
        })}

        <div className="surface-panel rounded-2xl border-0 px-5 py-4">
          <div className="mb-1 text-sm font-semibold">Auth Endpoint Limits</div>
          <p className="text-muted-foreground mb-3 text-xs">
            Per-IP limits for public authentication and Autoscan endpoints. These apply in addition
            to the global and shared per-IP budgets above.
          </p>
          <div className="space-y-4">
            {Object.keys(AUTH_ENDPOINT_LABELS).map((endpoint) => {
              const epConfig = config.auth_endpoints[endpoint] ?? DEFAULT_AUTH_ENDPOINT;
              return (
                <div key={endpoint}>
                  <div className="text-muted-foreground mb-2 text-xs font-medium">
                    {AUTH_ENDPOINT_LABELS[endpoint]}
                  </div>
                  <div className="grid gap-4 sm:grid-cols-2">
                    <div className="space-y-1">
                      <Label htmlFor={`${endpoint}-rpm`} className="text-sm font-medium">
                        Requests / Minute
                      </Label>
                      <Input
                        id={`${endpoint}-rpm`}
                        type="number"
                        min={1}
                        value={epConfig.requests_per_minute}
                        onChange={(e) =>
                          handleAuthEndpointChange(endpoint, "requests_per_minute", e.target.value)
                        }
                        className="w-full"
                      />
                    </div>
                    <div className="space-y-1">
                      <Label htmlFor={`${endpoint}-burst`} className="text-sm font-medium">
                        Burst
                      </Label>
                      <Input
                        id={`${endpoint}-burst`}
                        type="number"
                        min={1}
                        value={epConfig.burst}
                        onChange={(e) =>
                          handleAuthEndpointChange(endpoint, "burst", e.target.value)
                        }
                        className="w-full"
                      />
                    </div>
                  </div>
                </div>
              );
            })}
          </div>
        </div>

        <div className="pt-2">
          <Button onClick={handleSave} disabled={updateConfig.isPending}>
            {updateConfig.isPending ? "Saving..." : "Save Changes"}
          </Button>
        </div>
      </fieldset>
    </div>
  );
}
