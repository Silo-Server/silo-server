import { useId, useRef, useState, type ReactNode } from "react";
import { useIsMutating } from "@tanstack/react-query";
import { Link } from "react-router";
import { ArrowUpRight, Check, Copy, KeyRound, Loader2, X } from "lucide-react";

import type { PluginInstallation } from "@/api/types";
import { V2ProblemError } from "@/api/v2/request";
import { fieldIsVisible } from "@/components/admin/plugins/schemaFormUtils";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import { AdvancedSection } from "@/components/settings/AdvancedSection";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { Switch } from "@/components/ui/switch";
import {
  signInBindingMutationKey,
  useTestSignInConnection,
  useUpdateSignInBinding,
  type AuthConnectionTestResult,
} from "@/hooks/queries/admin/externalSignIn";
import { copyTextToClipboard } from "@/lib/clipboard";
import {
  activeSignInInstallation,
  adminSignInErrorText,
  authBindingOf,
  authCapabilityOf,
  primarySignInInstallations,
  authProviderLabel,
  authProviderName,
  savedAutoProvision,
  signInBindingWrite,
} from "@/lib/externalSignInAdmin";
import { pluginPagePath } from "@/lib/pluginPresentation";
import { cn } from "@/lib/utils";
import { FeedbackLine, type Feedback } from "@/components/admin/FeedbackLine";

import { SETTINGS_CONTROL_WIDTH, SettingFieldRow, SettingFieldStatus } from "./SettingField";
import { SignInConfigField } from "./SignInConfigField";
import {
  fieldChanged,
  isFieldMissing,
  missingSignInSetup,
  type SignInFieldGroup,
  type SignInSetupLayout,
} from "./signInSetup";
import type { SignInProviderDrafts } from "./useSignInProviderDrafts";

const CATALOG_PATH = "/admin/plugins?tab=catalog";

type SignInMode = "oauth" | "credentials" | "other";

function signInModeOf(installation: PluginInstallation): SignInMode {
  const mode = authCapabilityOf(installation)?.sign_in_mode;
  return mode === "oauth" ? "oauth" : mode === "credentials" ? "credentials" : "other";
}

const PROTOCOL_NAME: Record<SignInMode, string | null> = {
  oauth: "OpenID Connect",
  credentials: "LDAP",
  other: null,
};

const PROTOCOL_EXAMPLES: Record<SignInMode, string> = {
  oauth: "authentik, Authelia, Keycloak, Pocket ID, Zitadel, Entra ID…",
  credentials: "Active Directory, lldap, FreeIPA, OpenLDAP…",
  other: "",
};

/**
 * What the page calls a sign-in plugin: its protocol ("OpenID Connect") when
 * it is the only one installed for that protocol, else the plugin's name.
 */
function providerTitle(installation: PluginInstallation, all: PluginInstallation[]): string {
  const mode = signInModeOf(installation);
  const protocol = PROTOCOL_NAME[mode];
  const sameMode = all.filter((candidate) => signInModeOf(candidate) === mode).length;
  return protocol && sameMode === 1 ? protocol : authProviderName(installation);
}

type ProviderState = { word: string; tone: "ok" | "warn" | "off" };

function providerState(installation: PluginInstallation, missing: string[]): ProviderState {
  if (!installation.enabled) return { word: "Plugin turned off", tone: "off" };
  if (authBindingOf(installation)?.enabled === true) {
    return missing.length > 0
      ? { word: "On, needs setup", tone: "warn" }
      : { word: "On", tone: "ok" };
  }
  return missing.length > 0 ? { word: "Needs setup", tone: "warn" } : { word: "Off", tone: "off" };
}

function StateDot({ tone }: { tone: ProviderState["tone"] }) {
  return (
    <span
      aria-hidden="true"
      className={cn(
        "size-2 shrink-0 rounded-full",
        tone === "ok"
          ? "bg-emerald-500"
          : tone === "warn"
            ? "bg-amber-500"
            : "bg-muted-foreground/40",
      )}
    />
  );
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
  const valueId = useId();
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
      htmlFor={valueId}
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
      {/* The whole address wraps instead of being cut off, so it can be read
          and checked against the provider's form. */}
      <textarea
        id={valueId}
        readOnly
        rows={Math.max(1, Math.ceil(value.length / 34))}
        value={value}
        aria-describedby={descriptionId}
        onFocus={(event) => event.currentTarget.select()}
        className={cn(
          SETTINGS_CONTROL_WIDTH,
          "border-muted-foreground/25 bg-background [field-sizing:content] resize-none rounded-md border px-3 py-2 font-mono text-xs break-all outline-none",
          "focus-visible:border-ring focus-visible:ring-ring/50 focus-visible:ring-[3px]",
        )}
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
    <div className="space-y-2" data-testid="sign-in-test-result">
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

/** A numbered setup step. */
function Step({
  number,
  title,
  description,
  children,
}: {
  number: number;
  title: string;
  description?: string;
  children: ReactNode;
}) {
  const headingId = useId();
  return (
    <section aria-labelledby={headingId} className="pt-5" data-testid="sign-in-step">
      <div className="flex items-center gap-2.5">
        <span
          aria-hidden="true"
          className="border-border bg-muted/40 text-muted-foreground grid size-[22px] shrink-0 place-items-center rounded-full border text-[11.5px] font-semibold"
        >
          {number}
        </span>
        <h4 id={headingId} className="text-sm font-semibold">
          <span className="sr-only">Step {number}: </span>
          {title}
        </h4>
      </div>
      {description ? (
        <p className="text-muted-foreground mt-1 ml-[32px] text-xs leading-relaxed">
          {description}
        </p>
      ) : null}
      <div className="settings-field-list ml-[32px]">{children}</div>
    </section>
  );
}

/**
 * Turning a provider on or off. Both apply at once on every node, outside
 * the save bar: they change who can sign in, so turning off asks first.
 * The binding is written as a whole row, so neither runs while another
 * binding write or the page's save (which may write the binding) is in
 * flight; otherwise one write could put back what the other changed.
 */
function useBindingSwitch({
  installation,
  label,
  localLoginOn,
  pageSaving,
  report,
}: {
  installation: PluginInstallation;
  label: string;
  localLoginOn: boolean;
  pageSaving: boolean;
  report: (feedback: Feedback) => void;
}) {
  const updateBinding = useUpdateSignInBinding();
  const bindingWrites = useIsMutating({ mutationKey: signInBindingMutationKey });
  const [confirmOff, setConfirmOff] = useState(false);

  function write(enabled: boolean, success: string) {
    updateBinding.mutate(
      {
        installationId: installation.id,
        body: signInBindingWrite(installation, { enabled }),
      },
      {
        onSuccess: () => report({ tone: "ok", text: success }),
        onError: (error) =>
          report({ tone: "error", text: adminSignInErrorText(error, `Couldn't change ${label}.`) }),
      },
    );
  }

  const dialog = (
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
        write(
          false,
          localLoginOn
            ? `${label} is off. Only Silo passwords sign in now.`
            : `${label} is off. Password sign-in is off too, so only break-glass admins can sign in now.`,
        )
      }
      isPending={updateBinding.isPending}
    />
  );

  return {
    pending: bindingWrites > 0 || pageSaving,
    turnOn: () => write(true, `${label} is on. People can sign in with it now.`),
    askOff: () => setConfirmOff(true),
    dialog,
  };
}

interface ProviderPanelProps {
  installation: PluginInstallation;
  title: string;
  layout: SignInSetupLayout;
  drafts: SignInProviderDrafts;
  /** Whether Silo passwords sign in (auth.local_password_login). */
  localLoginOn: boolean;
  publicUrlSet: boolean;
  connectionTestServed: boolean;
  /** Another installation's binding is on, so this one cannot be turned on. */
  otherActive: string | null;
}

/**
 * One sign-in plugin as guided setup: register Silo at the provider (OpenID
 * Connect), the plugin's own steps, the connection test, and the login
 * button, with what most servers leave alone under Advanced. Field edits wait
 * for the page's save bar.
 */
function ProviderPanel({
  installation,
  title,
  layout,
  drafts,
  localLoginOn,
  publicUrlSet,
  connectionTestServed,
  otherActive,
}: ProviderPanelProps) {
  const capability = authCapabilityOf(installation)!;
  const binding = authBindingOf(installation);
  // Turning the binding on or off is about the provider people sign in
  // with, so those say its button label ("Keycloak"), not the plugin's name.
  const label = authProviderLabel(installation);
  const enabled = binding?.enabled === true;
  const mode = signInModeOf(installation);
  const missing = missingSignInSetup(layout);
  const state = providerState(installation, missing);
  const unsaved = drafts.changeCountOf(installation);
  const testConnection = useTestSignInConnection();
  const [feedback, setFeedback] = useState<Feedback>(null);
  const [testResult, setTestResult] = useState<AuthConnectionTestResult | null>(null);
  const [testError, setTestError] = useState<string | null>(null);
  const [testUsername, setTestUsername] = useState(() => {
    const ref = layout.testUsername;
    const saved = ref ? layout.entries.get(ref.schemaKey)?.saved?.[ref.field.key] : undefined;
    return typeof saved === "string" ? saved : "";
  });
  const statusRef = useRef<HTMLParagraphElement>(null);
  const testHeadingRef = useRef<HTMLHeadingElement>(null);
  const testErrorRef = useRef<HTMLParagraphElement>(null);
  const autoProvisionId = useId();
  const autoProvisionDescription = useId();
  const testUsernameId = useId();
  const headingId = useId();
  const idPrefix = `sign-in-${installation.id}`;

  // Turn on and Turn off replace the control the admin used, so focus moves
  // to the line that says what happened.
  function report(next: Feedback) {
    setFeedback(next);
    requestAnimationFrame(() => statusRef.current?.focus());
  }

  const bindingSwitch = useBindingSwitch({
    installation,
    label,
    localLoginOn,
    pageSaving: drafts.saving,
    report,
  });

  const offersTest = connectionTestServed && capability.metadata?.connection_test === true;
  const turnOnBlocked =
    !installation.enabled || otherActive !== null || missing.length > 0 || unsaved > 0;

  function runTest() {
    setTestResult(null);
    setTestError(null);
    const ref = layout.testUsername;
    testConnection.mutate(
      {
        installationId: installation.id,
        capabilityId: capability.id,
        config: drafts.stagedForTest(
          installation,
          ref
            ? { schemaKey: ref.schemaKey, fieldKey: ref.field.key, value: testUsername }
            : undefined,
        ),
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

  function renderGroupFields(group: SignInFieldGroup) {
    const entry = layout.entries.get(group.schemaKey)!;
    const values = drafts.valuesOf(installation, entry);
    const draft = drafts.draftOf(installation, group.schemaKey);
    return group.fields
      .filter((field) => fieldIsVisible(entry.descriptor, field, values))
      .map((field) => (
        <SignInConfigField
          key={`${group.schemaKey}.${field.key}`}
          field={field}
          values={values}
          idPrefix={`${idPrefix}-${group.schemaKey}`}
          dirty={fieldChanged(entry, draft, field)}
          secretSaved={entry.configuredSecrets.includes(field.key)}
          clearing={draft?.clearSecrets.includes(field.key) ?? false}
          onChange={(value) => drafts.setField(installation, entry, field.key, value)}
          onToggleClear={() => drafts.toggleClearSecret(installation, entry, field.key)}
        />
      ));
  }

  function groupNeedsAttention(group: SignInFieldGroup): boolean {
    const entry = layout.entries.get(group.schemaKey)!;
    const draft = drafts.draftOf(installation, group.schemaKey);
    return group.fields.some(
      (field) => fieldChanged(entry, draft, field) || isFieldMissing(entry, field),
    );
  }

  const callbackUrl =
    capability.callback_url || binding?.callback_url || testResult?.callback_url || "";
  const postLogoutUrl =
    capability.post_logout_redirect_url || binding?.post_logout_redirect_url || "";

  const autoProvision = drafts.autoProvisionOf(installation);
  const autoProvisionRow = (
    <SettingFieldRow
      key="auto-provision"
      label="Create accounts on first sign-in"
      htmlFor={autoProvisionId}
      dirty={autoProvision !== savedAutoProvision(installation)}
      description="People the provider lets in get a Silo account the first time they sign in. Off: only people whose Silo account is already connected can sign in."
      descriptionId={autoProvisionDescription}
    >
      <Switch
        id={autoProvisionId}
        aria-describedby={autoProvisionDescription}
        checked={autoProvision}
        onCheckedChange={(checked) => drafts.setAutoProvision(installation, checked)}
      />
    </SettingFieldRow>
  );

  const steps: { key: string; title: string; description?: string; body: ReactNode }[] = [];
  if (mode === "oauth") {
    steps.push({
      key: "register",
      title: "Register Silo at your provider",
      description: "Create a confidential client for Silo and add this redirect URI to it.",
      body: (
        <>
          {!publicUrlSet ? (
            <div className="py-3.5">
              <SettingFieldStatus tone="warn">
                <span>
                  The redirect URI needs the server's public URL. Set it in{" "}
                  <Link to="/admin/settings/general" className="underline underline-offset-4">
                    General settings
                  </Link>
                  .
                </span>
              </SettingFieldStatus>
            </div>
          ) : null}
          {callbackUrl ? (
            <CopyField
              label="Redirect URI"
              value={callbackUrl}
              description="Also called the callback URL."
            />
          ) : null}
          {postLogoutUrl ? (
            <CopyField
              label="Post-logout redirect URI"
              value={postLogoutUrl}
              description="Only needed if you turn on signing out at the provider (under Advanced)."
            />
          ) : null}
        </>
      ),
    });
  }
  layout.steps.forEach((group, index) => {
    const last = index === layout.steps.length - 1;
    steps.push({
      key: group.id,
      title: group.title,
      description: group.description,
      body: (
        <>
          {renderGroupFields(group)}
          {last ? autoProvisionRow : null}
        </>
      ),
    });
  });
  if (layout.steps.length === 0) {
    steps.push({ key: "accounts", title: "Choose who can sign in", body: autoProvisionRow });
  }
  if (offersTest) {
    steps.push({
      key: "test",
      title: "Test the connection",
      description:
        unsaved > 0
          ? "Tests your unsaved changes over the saved configuration. Nothing is saved."
          : "Tests the saved configuration. Nothing is saved.",
      body: (
        <div className="space-y-3 py-3.5">
          <div className="flex flex-wrap items-end gap-2">
            {layout.testUsername ? (
              <div className="min-w-0 space-y-1">
                <label htmlFor={testUsernameId} className="text-muted-foreground block text-xs">
                  Look up a user (optional)
                </label>
                <Input
                  id={testUsernameId}
                  value={testUsername}
                  onChange={(event) => setTestUsername(event.target.value)}
                  placeholder="Username"
                  autoComplete="off"
                  className="w-56"
                />
              </div>
            ) : null}
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
          </div>
          {layout.testUsername ? (
            <p className="text-muted-foreground text-xs">
              With a username, the test also finds that person and shows their groups and the role
              they would get. No password is used.
            </p>
          ) : null}
          <div aria-live="polite">
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
      ),
    });
  }
  if (layout.loginName) {
    const ref = layout.loginName;
    const entry = layout.entries.get(ref.schemaKey)!;
    const values = drafts.valuesOf(installation, entry);
    const typed = String(values[ref.field.key] ?? "").trim();
    const shown = typed || ref.field.placeholder || label;
    steps.push({
      key: "login-name",
      title: "Login button",
      body: (
        <>
          <SignInConfigField
            field={ref.field}
            values={values}
            idPrefix={`${idPrefix}-${ref.schemaKey}`}
            dirty={fieldChanged(entry, drafts.draftOf(installation, ref.schemaKey), ref.field)}
            onChange={(value) => drafts.setField(installation, entry, ref.field.key, value)}
            label={mode === "credentials" ? "Directory name" : "Provider name"}
            description={
              mode === "oauth"
                ? "Shown as “Sign in with …” on the login page and in the apps."
                : "Shown on the login page and in the apps where people choose how to sign in."
            }
          />
          {mode === "oauth" ? (
            <SettingFieldRow label="Preview">
              <span
                aria-hidden="true"
                className="border-border bg-muted/30 inline-flex items-center gap-2 rounded-lg border px-4 py-2 text-sm font-medium"
              >
                <KeyRound className="size-4" />
                Sign in with {shown}
              </span>
              <span className="sr-only">The login button reads: Sign in with {shown}</span>
            </SettingFieldRow>
          ) : null}
        </>
      ),
    });
  }

  let blockedReason: ReactNode = null;
  if (!enabled) {
    if (!installation.enabled) {
      blockedReason = (
        <>
          The plugin is turned off. Turn it on from its{" "}
          <Link
            to={pluginPagePath(installation.plugin_id)}
            className="text-foreground underline underline-offset-4"
          >
            plugin page
          </Link>{" "}
          first.
        </>
      );
    } else if (otherActive !== null) {
      blockedReason = `${otherActive} is the sign-in provider now. A server has one at a time: turn it off to use ${title} instead.`;
    } else if (unsaved > 0) {
      blockedReason = "Save your changes to turn it on.";
    }
  }

  return (
    <section aria-labelledby={headingId} className="pb-1" data-testid="sign-in-provider">
      <div className="flex flex-wrap items-start justify-between gap-3 pt-5">
        <div className="min-w-0 space-y-1">
          <h3 id={headingId} className="text-base font-semibold">
            {title}
          </h3>
          <p className="text-muted-foreground flex items-center gap-1.5 text-sm">
            <StateDot tone={state.tone} />
            <span data-testid="sign-in-provider-state">{state.word}</span>
            {enabled && installation.enabled ? (
              <>
                <span aria-hidden="true">·</span>
                <span>
                  {mode === "oauth"
                    ? `the login page shows “Sign in with ${label}”`
                    : `shown as “${label}” on the login page`}
                </span>
              </>
            ) : null}
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
              disabled={bindingSwitch.pending}
              onClick={bindingSwitch.askOff}
            >
              Turn off
            </Button>
          ) : (
            <Button
              type="button"
              size="sm"
              disabled={bindingSwitch.pending || turnOnBlocked}
              onClick={bindingSwitch.turnOn}
            >
              {bindingSwitch.pending ? (
                <Loader2 className="animate-spin" aria-hidden="true" />
              ) : null}
              Turn on
            </Button>
          )}
        </div>
      </div>

      <FeedbackLine feedback={feedback} focusRef={statusRef} />
      {blockedReason ? <p className="text-muted-foreground pt-1 text-sm">{blockedReason}</p> : null}
      {missing.length > 0 ? (
        <p className="pt-1 text-sm text-amber-600 dark:text-amber-400">
          Needs setup: fill in {missing.join(", ")} and save
          {enabled ? "." : " to turn it on."}
        </p>
      ) : null}

      {steps.map((step, index) => (
        <Step key={step.key} number={index + 1} title={step.title} description={step.description}>
          {step.body}
        </Step>
      ))}

      {layout.advanced.length > 0 ? (
        <div className="pt-6">
          <h4 className="text-sm font-semibold">Advanced</h4>
          <p className="text-muted-foreground text-xs">The defaults work for most providers.</p>
          <div className="pt-1">
            {layout.advanced.map((group) => (
              <AdvancedSection
                key={group.id}
                id={`sign-in.${installation.plugin_id}.${group.id}`}
                title={group.title}
                count={group.fields.length}
                forceOpen={groupNeedsAttention(group)}
              >
                {group.description ? (
                  <p className="settings-field-note text-muted-foreground pt-1 pb-1 text-xs leading-relaxed">
                    {group.description}
                  </p>
                ) : null}
                {renderGroupFields(group)}
              </AdvancedSection>
            ))}
          </div>
        </div>
      ) : null}

      {layout.unsupported.length > 0 ? (
        <p className="text-muted-foreground pt-4 text-sm">
          Some of this plugin's settings can only be changed on its{" "}
          <Link
            to={pluginPagePath(installation.plugin_id)}
            className="text-foreground underline underline-offset-4"
          >
            plugin page
          </Link>
          .
        </p>
      ) : null}

      {bindingSwitch.dialog}
    </section>
  );
}

/** "None": only Silo passwords sign in, or what to turn off to get there. */
function NoProviderPanel({
  active,
  localLoginOn,
  pageSaving,
}: {
  active: PluginInstallation | undefined;
  localLoginOn: boolean;
  pageSaving: boolean;
}) {
  if (!active) {
    return (
      <p className="text-muted-foreground pt-5 text-sm">
        No sign-in provider is on, so people sign in with Silo passwords. Pick a provider above to
        set it up.
      </p>
    );
  }
  return (
    <TurnOffActiveProvider active={active} localLoginOn={localLoginOn} pageSaving={pageSaving} />
  );
}

function TurnOffActiveProvider({
  active,
  localLoginOn,
  pageSaving,
}: {
  active: PluginInstallation;
  localLoginOn: boolean;
  pageSaving: boolean;
}) {
  const [feedback, setFeedback] = useState<Feedback>(null);
  const statusRef = useRef<HTMLParagraphElement>(null);
  const label = authProviderLabel(active);
  const bindingSwitch = useBindingSwitch({
    installation: active,
    label,
    localLoginOn,
    pageSaving,
    report: (next) => {
      setFeedback(next);
      requestAnimationFrame(() => statusRef.current?.focus());
    },
  });
  return (
    <div className="pt-5">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <p className="text-muted-foreground text-sm">
          {label} is on. Turn it off to sign in with Silo passwords only.
        </p>
        <Button
          type="button"
          size="sm"
          variant="outline"
          disabled={bindingSwitch.pending}
          onClick={bindingSwitch.askOff}
        >
          Turn off {label}
        </Button>
      </div>
      <FeedbackLine feedback={feedback} focusRef={statusRef} />
      {bindingSwitch.dialog}
    </div>
  );
}

interface SignInProviderSlotProps {
  installations: PluginInstallation[] | undefined;
  loading: boolean;
  failed: boolean;
  drafts: SignInProviderDrafts;
  localLoginOn: boolean;
  publicUrlSet: boolean;
  connectionTestServed: boolean;
}

type Selection = number | "none";

/**
 * The server's one external sign-in provider: a choice between the installed
 * sign-in plugins (and none), and guided setup for the one chosen. Choosing
 * only changes what is shown; Turn on is what makes a provider the server's.
 */
export function SignInProviderSlot({
  installations,
  loading,
  failed,
  drafts,
  localLoginOn,
  publicUrlSet,
  connectionTestServed,
}: SignInProviderSlotProps) {
  const [picked, setPicked] = useState<Selection | null>(null);
  const groupLabelId = useId();

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

  const candidates = primarySignInInstallations(installations);
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
  // Until the admin picks, show the provider that is on, else one with setup
  // under way, else the first installed. "None" is only ever picked.
  const fallback: Selection = (
    active ??
    candidates.find((candidate) => (candidate.global_configs ?? []).length > 0) ??
    candidates[0]!
  ).id;
  const selection: Selection =
    picked === "none" || candidates.some((candidate) => candidate.id === picked)
      ? (picked as Selection)
      : fallback;
  const selected = candidates.find((candidate) => candidate.id === selection);

  const choices: { id: Selection; title: string; detail: string; state?: ProviderState }[] = [
    ...candidates.map((installation) => {
      const layout = drafts.layoutOf(installation);
      return {
        id: installation.id as Selection,
        title: providerTitle(installation, candidates),
        detail: PROTOCOL_EXAMPLES[signInModeOf(installation)] || authProviderName(installation),
        state: providerState(installation, missingSignInSetup(layout)),
      };
    }),
    { id: "none", title: "None", detail: "Only Silo passwords." },
  ];

  return (
    <div className="pb-2">
      <span id={groupLabelId} className="sr-only">
        Sign-in provider to show
      </span>
      <div
        role="group"
        aria-labelledby={groupLabelId}
        className="grid gap-2.5 pt-3.5 sm:grid-cols-[repeat(auto-fit,minmax(11rem,1fr))]"
      >
        {choices.map((choice) => {
          const on = choice.id === selection;
          return (
            <button
              key={choice.id}
              type="button"
              aria-pressed={on}
              onClick={() => setPicked(choice.id)}
              className={cn(
                "bg-muted/20 hover:bg-muted/40 rounded-xl border px-3.5 py-3 text-left transition-colors",
                on
                  ? "border-[var(--settings-accent)] ring-1 ring-[var(--settings-accent)]"
                  : "border-border",
              )}
            >
              <span className="flex items-center justify-between gap-2">
                <span className="text-sm font-semibold">{choice.title}</span>
                {choice.state ? (
                  <span className="text-muted-foreground border-border inline-flex shrink-0 items-center gap-1.5 rounded-full border px-2 py-0.5 text-[11px]">
                    <StateDot tone={choice.state.tone} />
                    {choice.state.word}
                  </span>
                ) : null}
              </span>
              <span className="text-muted-foreground mt-1 block text-xs leading-snug">
                {choice.detail}
              </span>
            </button>
          );
        })}
      </div>
      {active && candidates.length > 1 ? (
        <p className="text-muted-foreground pt-2 text-xs">
          A server uses one provider at a time. To switch, turn off{" "}
          {providerTitle(active, candidates)} first.
        </p>
      ) : null}

      {selected ? (
        <ProviderPanel
          key={selected.id}
          installation={selected}
          title={providerTitle(selected, candidates)}
          layout={drafts.layoutOf(selected)}
          drafts={drafts}
          localLoginOn={localLoginOn}
          publicUrlSet={publicUrlSet}
          connectionTestServed={connectionTestServed}
          otherActive={
            active && active.id !== selected.id ? providerTitle(active, candidates) : null
          }
        />
      ) : (
        <NoProviderPanel active={active} localLoginOn={localLoginOn} pageSaving={drafts.saving} />
      )}
    </div>
  );
}
