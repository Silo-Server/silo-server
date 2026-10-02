import { useId, useRef, useState } from "react";
import { Link } from "react-router";
import { ArrowUpRight, Check, Copy, Loader2, X } from "lucide-react";

import type { PluginInstallation } from "@/api/types";
import { V2ProblemError } from "@/api/v2/request";
import { PluginConfigForm } from "@/components/admin/plugins/PluginConfigForm";
import { humanizeConfigKey } from "@/components/admin/plugins/configSchemaAdminForm";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import { AdvancedSection } from "@/components/settings/AdvancedSection";
import { SettingsSubheading } from "@/components/settings/SettingsSubheading";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { Switch } from "@/components/ui/switch";
import {
  useSaveSignInPluginConfig,
  useTestSignInConnection,
  useUpdateSignInBinding,
  type AuthConnectionTestResult,
  type AuthConnectionTestStaged,
} from "@/hooks/queries/admin/externalSignIn";
import { copyTextToClipboard } from "@/lib/clipboard";
import {
  activeSignInInstallation,
  adminSignInErrorText,
  authBindingOf,
  authCapabilityOf,
  authPluginInstallations,
  authProviderLabel,
  authProviderName,
} from "@/lib/externalSignInAdmin";
import { pluginPagePath } from "@/lib/pluginPresentation";
import { cn } from "@/lib/utils";
import { FeedbackLine, type Feedback } from "@/components/admin/FeedbackLine";

import { SettingFieldRow, SettingFieldStatus } from "./SettingField";

const CATALOG_PATH = "/admin/plugins?tab=catalog";

function missingRequiredConfig(installation: PluginInstallation): string[] {
  const saved = new Set((installation.global_configs ?? []).map((entry) => entry.key));
  return (installation.global_config_schema ?? [])
    .filter((schema) => schema.required && !saved.has(schema.key))
    .map((schema) => schema.title?.trim() || humanizeConfigKey(schema.key));
}

/** A read-only URL an admin registers at the provider, with a copy button. */
function CopyField({
  label,
  value,
  description,
}: {
  label: string;
  value: string;
  description: string;
}) {
  const inputId = useId();
  const descriptionId = useId();
  const [copied, setCopied] = useState<"ok" | "failed" | null>(null);

  async function copy() {
    try {
      await copyTextToClipboard(value);
      setCopied("ok");
    } catch {
      setCopied("failed");
    }
  }

  return (
    <SettingFieldRow
      label={label}
      htmlFor={inputId}
      description={description}
      descriptionId={descriptionId}
      status={
        copied === "ok" ? (
          <SettingFieldStatus>Copied</SettingFieldStatus>
        ) : copied === "failed" ? (
          <SettingFieldStatus tone="warn">
            Couldn't copy. Select the address and copy it by hand.
          </SettingFieldStatus>
        ) : null
      }
    >
      <Input
        id={inputId}
        readOnly
        value={value}
        aria-describedby={descriptionId}
        onFocus={(event) => event.currentTarget.select()}
        className="border-muted-foreground/25 w-full font-mono text-xs sm:w-[var(--settings-control-w)]"
      />
      <Button
        type="button"
        size="sm"
        variant="secondary"
        onClick={() => void copy()}
        aria-label={`Copy ${label.toLowerCase()}`}
      >
        <Copy aria-hidden="true" />
        Copy
      </Button>
    </SettingFieldRow>
  );
}

function testErrorText(error: unknown): string {
  if (error instanceof V2ProblemError && error.status === 409) return error.message;
  return adminSignInErrorText(error, "The connection test didn't run.");
}

/** The steps a connection test ran, each with its result. */
function ConnectionTestResult({
  result,
  headingRef,
}: {
  result: AuthConnectionTestResult;
  headingRef: React.RefObject<HTMLHeadingElement | null>;
}) {
  const failed = result.steps.filter((step) => !step.ok).length;
  return (
    <div className="space-y-2 pb-3.5" data-testid="sign-in-test-result">
      <h4
        ref={headingRef}
        tabIndex={-1}
        className={cn(
          "text-sm font-medium focus:outline-none",
          result.ok ? "text-green-600 dark:text-green-400" : "text-destructive",
        )}
      >
        {result.ok
          ? "Every check passed."
          : failed > 0
            ? `${failed} of ${result.steps.length} checks failed.`
            : "The test did not pass."}
      </h4>
      {result.steps.length > 0 ? (
        <ol className="space-y-1.5" aria-label="Connection test steps">
          {result.steps.map((step) => (
            <li key={step.id} className="flex items-start gap-2 text-sm">
              {step.ok ? (
                <Check
                  className="mt-0.5 size-4 shrink-0 text-green-600 dark:text-green-400"
                  aria-hidden="true"
                />
              ) : (
                <X className="text-destructive mt-0.5 size-4 shrink-0" aria-hidden="true" />
              )}
              <span className="min-w-0">
                <span className="font-medium">
                  {step.label}
                  <span className="sr-only">{step.ok ? ": passed" : ": failed"}</span>
                </span>
                {step.message ? (
                  <span className="text-muted-foreground block text-xs break-words">
                    {step.message}
                  </span>
                ) : null}
              </span>
            </li>
          ))}
        </ol>
      ) : null}
    </div>
  );
}

interface ProviderPanelProps {
  installation: PluginInstallation;
  /** Whether Silo passwords sign in (auth.local_password_login). */
  localLoginOn: boolean;
  publicUrlSet: boolean;
  connectionTestServed: boolean;
  /** Another installation's binding is on, so this one cannot be turned on. */
  otherActive: string | null;
}

/**
 * One sign-in plugin: its state, the URLs to register at the provider, its
 * binding switches, its configuration and the connection test. Binding and
 * configuration writes apply at once, like a provider tile.
 */
function ProviderPanel({
  installation,
  localLoginOn,
  publicUrlSet,
  connectionTestServed,
  otherActive,
}: ProviderPanelProps) {
  const capability = authCapabilityOf(installation)!;
  const binding = authBindingOf(installation);
  const name = authProviderName(installation);
  // Turning the binding on or off is about the provider people sign in
  // with, so those say its button label ("Keycloak"), not the plugin's name.
  const label = authProviderLabel(installation);
  const enabled = binding?.enabled === true;
  const missing = missingRequiredConfig(installation);
  const updateBinding = useUpdateSignInBinding();
  const saveConfig = useSaveSignInPluginConfig();
  const testConnection = useTestSignInConnection();
  const [feedback, setFeedback] = useState<Feedback>(null);
  const [confirmOff, setConfirmOff] = useState(false);
  const [drafts, setDrafts] = useState<Record<string, AuthConnectionTestStaged>>({});
  const [testResult, setTestResult] = useState<AuthConnectionTestResult | null>(null);
  const [testError, setTestError] = useState<string | null>(null);
  const statusRef = useRef<HTMLParagraphElement>(null);
  const testHeadingRef = useRef<HTMLHeadingElement>(null);
  const testErrorRef = useRef<HTMLParagraphElement>(null);
  const autoProvisionId = useId();
  const autoProvisionDescription = useId();
  const headingId = useId();

  // The capability says how it signs in, with or without a binding row.
  const isOAuth = capability.sign_in_mode === "oauth";
  const isPassword = capability.sign_in_mode === "credentials";
  const offersTest = connectionTestServed && capability.metadata?.connection_test === true;
  const busy = updateBinding.isPending || saveConfig.isPending;
  const stateWord = !installation.enabled
    ? "Plugin turned off"
    : enabled
      ? missing.length > 0
        ? "On, needs setup"
        : "On"
      : "Off";

  function report(next: Feedback, moveFocus = false) {
    setFeedback(next);
    // Turn on and Turn off replace the control the admin used, so focus
    // moves to the line that says what happened. Controls that stay put
    // keep focus; the line is a live region, so the result is still heard.
    if (moveFocus) requestAnimationFrame(() => statusRef.current?.focus());
  }

  function writeBinding(change: { enabled?: boolean; auto_provision?: boolean }, success: string) {
    const moveFocus = change.enabled !== undefined;
    updateBinding.mutate(
      {
        installationId: installation.id,
        body: {
          capability_id: capability.id,
          enabled: change.enabled ?? enabled,
          display_order: binding?.display_order ?? 1,
          auto_provision: change.auto_provision ?? binding?.auto_provision ?? true,
          default_login: binding?.default_login ?? false,
        },
      },
      {
        onSuccess: () => report({ tone: "ok", text: success }, moveFocus),
        onError: (error) =>
          report(
            {
              tone: "error",
              text: adminSignInErrorText(error, `Couldn't change ${label}.`),
            },
            moveFocus,
          ),
      },
    );
  }

  function runTest() {
    setTestResult(null);
    setTestError(null);
    testConnection.mutate(
      {
        installationId: installation.id,
        capabilityId: capability.id,
        config: Object.values(drafts),
      },
      {
        onSuccess: (result) => {
          setTestResult(result);
          requestAnimationFrame(() => testHeadingRef.current?.focus());
        },
        onError: (error) => {
          setTestError(testErrorText(error));
          requestAnimationFrame(() => testErrorRef.current?.focus());
        },
      },
    );
  }

  const callbackUrl =
    capability.callback_url || binding?.callback_url || testResult?.callback_url || "";
  const postLogoutUrl =
    capability.post_logout_redirect_url || binding?.post_logout_redirect_url || "";
  const stagedCount = Object.keys(drafts).length;

  return (
    <section aria-labelledby={headingId} className="space-y-1" data-testid="sign-in-provider">
      <div className="flex flex-wrap items-start justify-between gap-3 pb-3">
        <div className="min-w-0 space-y-1">
          <h3 id={headingId} className="text-base font-semibold">
            {name}
          </h3>
          <p className="text-muted-foreground flex items-center gap-1.5 text-sm">
            <span
              aria-hidden="true"
              className={cn(
                "size-2 rounded-full",
                enabled && installation.enabled
                  ? missing.length > 0
                    ? "bg-amber-500"
                    : "bg-emerald-500"
                  : "bg-muted-foreground/40",
              )}
            />
            <span data-testid="sign-in-provider-state">{stateWord}</span>
            <span aria-hidden="true">·</span>
            <span>
              {isOAuth ? "OpenID Connect" : isPassword ? "Directory (LDAP)" : "Sign-in plugin"}
            </span>
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Button asChild size="sm" variant="ghost">
            <Link to={pluginPagePath(installation.plugin_id)}>
              Plugin page
              <ArrowUpRight aria-hidden="true" />
            </Link>
          </Button>
          {enabled ? (
            <Button
              type="button"
              size="sm"
              variant="outline"
              disabled={busy}
              onClick={() => setConfirmOff(true)}
            >
              Turn off
            </Button>
          ) : (
            <Button
              type="button"
              size="sm"
              disabled={busy || !installation.enabled || otherActive !== null}
              onClick={() =>
                writeBinding({ enabled: true }, `${label} is on. People can sign in with it now.`)
              }
            >
              {updateBinding.isPending ? (
                <Loader2 className="animate-spin" aria-hidden="true" />
              ) : null}
              Turn on
            </Button>
          )}
        </div>
      </div>

      <FeedbackLine feedback={feedback} focusRef={statusRef} />

      {!installation.enabled ? (
        <p className="text-muted-foreground text-sm">
          The plugin is turned off. Turn it on from its{" "}
          <Link
            to={pluginPagePath(installation.plugin_id)}
            className="text-foreground underline underline-offset-4"
          >
            plugin page
          </Link>{" "}
          first.
        </p>
      ) : null}
      {otherActive !== null && !enabled ? (
        <p className="text-muted-foreground text-sm">
          {otherActive} is the sign-in provider now. A server has one at a time: turn it off to use{" "}
          {name} instead.
        </p>
      ) : null}
      {missing.length > 0 ? (
        <p className="text-sm text-amber-600 dark:text-amber-400">
          Needs setup: save {missing.join(", ")} under Configuration below.
        </p>
      ) : null}

      {isOAuth && !publicUrlSet ? (
        <SettingFieldStatus tone="warn">
          <span>
            OpenID Connect sign-in needs the server's public URL for the redirect URI. Set it in{" "}
            <Link to="/admin/settings/general" className="underline underline-offset-4">
              General settings
            </Link>
            .
          </span>
        </SettingFieldStatus>
      ) : null}

      <div className="settings-field-list">
        {isOAuth && callbackUrl ? (
          <CopyField
            label="Redirect URI"
            value={callbackUrl}
            description="Register this at the provider as the client's redirect (callback) URI."
          />
        ) : null}
        {isOAuth && postLogoutUrl ? (
          <CopyField
            label="Post-logout redirect URI"
            value={postLogoutUrl}
            description="Register this at the provider too if you turn on signing out at the provider."
          />
        ) : null}

        <SettingFieldRow
          label="Create accounts on first sign-in"
          htmlFor={autoProvisionId}
          description="People the provider lets in get a Silo account the first time they sign in. Off: only people whose account is already connected can sign in."
          descriptionId={autoProvisionDescription}
        >
          <Switch
            id={autoProvisionId}
            aria-describedby={autoProvisionDescription}
            checked={binding?.auto_provision ?? true}
            disabled={busy || !binding}
            onCheckedChange={(checked) =>
              writeBinding(
                { auto_provision: checked },
                checked
                  ? "New people get an account on their first sign-in."
                  : "Only people with a connected account can sign in now.",
              )
            }
          />
        </SettingFieldRow>

        {offersTest ? (
          <div className="border-border/60 border-b py-3.5">
            <div className="flex flex-wrap items-center gap-3">
              <Button
                type="button"
                size="sm"
                variant="secondary"
                onClick={runTest}
                disabled={testConnection.isPending || !installation.enabled}
              >
                {testConnection.isPending ? (
                  <Loader2 className="animate-spin" aria-hidden="true" />
                ) : null}
                {testConnection.isPending ? "Testing..." : "Test connection"}
              </Button>
              <span className="text-muted-foreground text-xs">
                {stagedCount > 0
                  ? "Tests your unsaved changes below over the saved configuration. Nothing is saved."
                  : "Tests the saved configuration. Nothing is saved."}
              </span>
            </div>
            <div aria-live="polite" className="mt-3">
              {testResult ? (
                <ConnectionTestResult result={testResult} headingRef={testHeadingRef} />
              ) : null}
              {testError ? (
                <p
                  ref={testErrorRef}
                  tabIndex={-1}
                  role="alert"
                  className="text-destructive text-sm focus:outline-none"
                >
                  {testError}
                </p>
              ) : null}
            </div>
          </div>
        ) : null}
      </div>

      {(installation.global_config_schema ?? []).length > 0 ? (
        <AdvancedSection
          id={`sign-in.config.${installation.plugin_id}`}
          title="Configuration"
          count={installation.global_config_schema.length}
          forceOpen={missing.length > 0 || stagedCount > 0}
        >
          {installation.global_config_schema.map((schema) => {
            const saved = installation.global_configs?.find((entry) => entry.key === schema.key);
            return (
              <div key={schema.key} className="pb-3">
                <SettingsSubheading caption={schema.description || undefined}>
                  {schema.title?.trim() || humanizeConfigKey(schema.key)}
                </SettingsSubheading>
                <PluginConfigForm
                  bare
                  idPrefix={`sign-in-${installation.id}-${schema.key}`}
                  schema={schema}
                  value={saved?.value}
                  configuredSecrets={saved?.configured_secrets}
                  isSaving={saveConfig.isPending}
                  onDraftChange={(key, value, clearSecrets) =>
                    setDrafts((current) => ({
                      ...current,
                      [key]: { key, value, clear_secrets: clearSecrets },
                    }))
                  }
                  onSave={(key, value, clearSecrets) =>
                    saveConfig.mutate(
                      { installationId: installation.id, key, value, clearSecrets },
                      {
                        onSuccess: () => {
                          setDrafts((current) => {
                            const next = { ...current };
                            delete next[key];
                            return next;
                          });
                          report({
                            tone: "ok",
                            text: `Saved ${schema.title?.trim() || humanizeConfigKey(key)}.`,
                          });
                        },
                        onError: (error) =>
                          report({
                            tone: "error",
                            text: adminSignInErrorText(error, "Couldn't save the configuration."),
                          }),
                      },
                    )
                  }
                />
              </div>
            );
          })}
        </AdvancedSection>
      ) : null}

      <ConfirmDialog
        open={confirmOff}
        onOpenChange={setConfirmOff}
        title={`Turn off ${label}?`}
        description={
          localLoginOn
            ? `People who sign in with ${label} can't sign in until it's back on. Their Silo accounts, and the connections to ${label}, stay.`
            : `Password sign-in is off, so only break-glass admins can sign in until ${label} is back on. Silo accounts, and their connections to ${label}, stay.`
        }
        confirmLabel="Turn off"
        variant="destructive"
        onConfirm={() =>
          writeBinding(
            { enabled: false },
            localLoginOn
              ? `${label} is off. Only Silo passwords sign in now.`
              : `${label} is off. Password sign-in is off too, so only break-glass admins can sign in now.`,
          )
        }
        isPending={updateBinding.isPending}
      />
    </section>
  );
}

interface SignInProviderSlotProps {
  installations: PluginInstallation[] | undefined;
  loading: boolean;
  failed: boolean;
  localLoginOn: boolean;
  publicUrlSet: boolean;
  connectionTestServed: boolean;
}

/**
 * The server's one external sign-in provider: the one that is on, or the
 * installed sign-in plugins to choose from, or where to install one.
 */
export function SignInProviderSlot({
  installations,
  loading,
  failed,
  localLoginOn,
  publicUrlSet,
  connectionTestServed,
}: SignInProviderSlotProps) {
  if (loading) {
    return (
      <div className="space-y-3 py-3.5" aria-busy="true">
        <Skeleton className="h-6 w-1/3" />
        <Skeleton className="h-16 w-full" />
      </div>
    );
  }
  if (failed) {
    return (
      <p className="text-destructive py-3.5 text-sm" role="alert">
        Couldn't read the installed plugins. Reload the page to try again.
      </p>
    );
  }

  const candidates = authPluginInstallations(installations);
  if (candidates.length === 0) {
    return (
      <p className="text-muted-foreground py-3.5 text-sm">
        No sign-in plugin is installed. Install{" "}
        <span className="text-foreground font-medium">OpenID Connect Sign-in</span> or{" "}
        <span className="text-foreground font-medium">LDAP Sign-in</span> from the{" "}
        <Link to={CATALOG_PATH} className="text-foreground underline underline-offset-4">
          plugin catalog
        </Link>
        , then set it up here.
      </p>
    );
  }

  const active = activeSignInInstallation(candidates);
  const shown = active ? [active, ...candidates.filter((c) => c.id !== active.id)] : candidates;

  return (
    <div className="divide-border/60 divide-y">
      {shown.map((installation) => (
        <div key={installation.id} className="py-4 first:pt-2 last:pb-2">
          <ProviderPanel
            installation={installation}
            localLoginOn={localLoginOn}
            publicUrlSet={publicUrlSet}
            connectionTestServed={connectionTestServed}
            otherActive={active && active.id !== installation.id ? authProviderName(active) : null}
          />
        </div>
      ))}
    </div>
  );
}
