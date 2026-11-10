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
import { useT, type Translate } from "@/i18n";

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
/** Decorative dot between a provider's state and its login page name. */
const SEPARATOR = "·";

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

function providerState(
  installation: PluginInstallation,
  missing: string[],
  t: Translate,
): ProviderState {
  if (!installation.enabled)
    return { word: t("web.admin.settings.signIn.provider.state.pluginOff"), tone: "off" };
  if (authBindingOf(installation)?.enabled === true) {
    return missing.length > 0
      ? { word: t("web.admin.settings.signIn.provider.state.onNeedsSetup"), tone: "warn" }
      : { word: t("web.admin.settings.signIn.provider.state.on"), tone: "ok" };
  }
  return missing.length > 0
    ? { word: t("web.admin.settings.signIn.provider.state.needsSetup"), tone: "warn" }
    : { word: t("web.admin.settings.signIn.provider.state.off"), tone: "off" };
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
  copyLabel,
  value,
  description,
}: {
  label: string;
  /** The copy button's accessible name. */
  copyLabel: string;
  value: string;
  description: string;
}) {
  const t = useT();
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
          <SettingFieldStatus>
            {t("web.admin.settings.signIn.provider.copy.copied")}
          </SettingFieldStatus>
        ) : copied === "failed" ? (
          <SettingFieldStatus tone="warn">
            {t("web.admin.settings.signIn.provider.copy.failed")}
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
        aria-label={copyLabel}
      >
        <Copy aria-hidden="true" />
        {t("web.admin.settings.signIn.provider.copy.button")}
      </Button>
    </SettingFieldRow>
  );
}

function testErrorText(error: unknown, t: Translate): string {
  if (error instanceof V2ProblemError && error.status === 409) return error.message;
  return adminSignInErrorText(error, t("web.admin.settings.signIn.provider.test.errorFallback"));
}

/** The steps a connection test ran, each with its result. */
function ConnectionTestResult({
  result,
  headingRef,
}: {
  result: AuthConnectionTestResult;
  headingRef: React.RefObject<HTMLHeadingElement | null>;
}) {
  const t = useT();
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
          ? t("web.admin.settings.signIn.provider.test.allPassed")
          : failed > 0
            ? t("web.admin.settings.signIn.provider.test.someFailed", {
                total: result.steps.length,
                failed: failed,
              })
            : t("web.admin.settings.signIn.provider.test.didNotPass")}
      </h4>
      {result.steps.length > 0 ? (
        <ol
          className="space-y-1.5"
          aria-label={t("web.admin.settings.signIn.provider.test.stepsLabel")}
        >
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
                  {t(
                    step.ok
                      ? "web.admin.settings.signIn.provider.test.stepPassed"
                      : "web.admin.settings.signIn.provider.test.stepFailed",
                    {
                      label: step.label,
                      hidden: (chunks) => <span className="sr-only">{chunks}</span>,
                    },
                  )}
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
  const t = useT();
  const headingId = useId();
  return (
    <section aria-labelledby={headingId} className="pt-5" data-testid="sign-in-step">
      <div className="flex items-center gap-2.5">
        <span
          aria-hidden="true"
          className="border-border bg-muted/40 text-muted-foreground grid size-[22px] shrink-0 place-items-center rounded-full border text-xs font-semibold"
        >
          {number}
        </span>
        <h4 id={headingId} className="text-sm font-semibold">
          {t("web.admin.settings.signIn.provider.step.heading", {
            number: number,
            title,
            hidden: (chunks) => <span className="sr-only">{chunks}</span>,
          })}
        </h4>
      </div>
      {description ? (
        <p className="text-muted-foreground ms-[32px] mt-1 text-xs leading-relaxed">
          {description}
        </p>
      ) : null}
      <div className="settings-field-list ms-[32px]">{children}</div>
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
  const t = useT();
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
          report({
            tone: "error",
            text: adminSignInErrorText(
              error,
              t("web.admin.settings.signIn.provider.binding.changeFailed", { name: label }),
            ),
          }),
      },
    );
  }

  const dialog = (
    <ConfirmDialog
      open={confirmOff}
      onOpenChange={setConfirmOff}
      title={t("web.admin.settings.signIn.provider.binding.turnOffTitle", { name: label })}
      description={
        localLoginOn
          ? t("web.admin.settings.signIn.provider.binding.turnOffDescription", { name: label })
          : t("web.admin.settings.signIn.provider.binding.turnOffDescriptionBreakGlass", {
              name: label,
            })
      }
      confirmLabel={t("web.admin.settings.signIn.provider.binding.turnOffConfirm")}
      variant="destructive"
      onConfirm={() =>
        write(
          false,
          localLoginOn
            ? t("web.admin.settings.signIn.provider.binding.turnedOff", { name: label })
            : t("web.admin.settings.signIn.provider.binding.turnedOffBreakGlass", { name: label }),
        )
      }
      isPending={updateBinding.isPending}
    />
  );

  return {
    pending: bindingWrites > 0 || pageSaving,
    turnOn: () =>
      write(true, t("web.admin.settings.signIn.provider.binding.turnedOn", { name: label })),
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
  const t = useT();
  const capability = authCapabilityOf(installation)!;
  const binding = authBindingOf(installation);
  // Turning the binding on or off is about the provider people sign in
  // with, so those say its button label ("Keycloak"), not the plugin's name.
  const label = authProviderLabel(installation);
  const enabled = binding?.enabled === true;
  const mode = signInModeOf(installation);
  const missing = missingSignInSetup(layout);
  const state = providerState(installation, missing, t);
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
          setTestError(testErrorText(error, t));
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
      label={t("web.admin.settings.signIn.provider.autoProvision.label")}
      htmlFor={autoProvisionId}
      dirty={autoProvision !== savedAutoProvision(installation)}
      description={t("web.admin.settings.signIn.provider.autoProvision.description")}
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
      title: t("web.admin.settings.signIn.provider.register.title"),
      description: t("web.admin.settings.signIn.provider.register.description"),
      body: (
        <>
          {!publicUrlSet ? (
            <div className="py-3.5">
              <SettingFieldStatus tone="warn">
                <span>
                  {t("web.admin.settings.signIn.provider.register.publicUrlMissing", {
                    link: (chunks) => (
                      <Link to="/admin/settings/general" className="underline underline-offset-4">
                        {chunks}
                      </Link>
                    ),
                  })}
                </span>
              </SettingFieldStatus>
            </div>
          ) : null}
          {callbackUrl ? (
            <CopyField
              label={t("web.admin.settings.signIn.provider.register.redirectUri")}
              copyLabel={t("web.admin.settings.signIn.provider.register.redirectUriCopy")}
              value={callbackUrl}
              description={t("web.admin.settings.signIn.provider.register.redirectUriDescription")}
            />
          ) : null}
          {postLogoutUrl ? (
            <CopyField
              label={t("web.admin.settings.signIn.provider.register.postLogoutUri")}
              copyLabel={t("web.admin.settings.signIn.provider.register.postLogoutUriCopy")}
              value={postLogoutUrl}
              description={t(
                "web.admin.settings.signIn.provider.register.postLogoutUriDescription",
              )}
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
    steps.push({
      key: "accounts",
      title: t("web.admin.settings.signIn.provider.accounts.title"),
      body: autoProvisionRow,
    });
  }
  if (offersTest) {
    steps.push({
      key: "test",
      title: t("web.admin.settings.signIn.provider.test.title"),
      description:
        unsaved > 0
          ? t("web.admin.settings.signIn.provider.test.descriptionUnsaved")
          : t("web.admin.settings.signIn.provider.test.descriptionSaved"),
      body: (
        <div className="space-y-3 py-3.5">
          <div className="flex flex-wrap items-end gap-2">
            {layout.testUsername ? (
              <div className="min-w-0 space-y-1">
                <label htmlFor={testUsernameId} className="text-muted-foreground block text-xs">
                  {t("web.admin.settings.signIn.provider.test.lookUpLabel")}
                </label>
                <Input
                  id={testUsernameId}
                  value={testUsername}
                  onChange={(event) => setTestUsername(event.target.value)}
                  placeholder={t("web.admin.settings.signIn.provider.test.usernamePlaceholder")}
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
              {testConnection.isPending
                ? t("web.admin.settings.signIn.provider.test.testing")
                : t("web.admin.settings.signIn.provider.test.run")}
            </Button>
          </div>
          {layout.testUsername ? (
            <p className="text-muted-foreground text-xs">
              {t("web.admin.settings.signIn.provider.test.lookUpNote")}
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
      title: t("web.admin.settings.signIn.provider.loginName.title"),
      body: (
        <>
          <SignInConfigField
            field={ref.field}
            values={values}
            idPrefix={`${idPrefix}-${ref.schemaKey}`}
            dirty={fieldChanged(entry, drafts.draftOf(installation, ref.schemaKey), ref.field)}
            onChange={(value) => drafts.setField(installation, entry, ref.field.key, value)}
            label={
              mode === "credentials"
                ? t("web.admin.settings.signIn.provider.loginName.directoryName")
                : t("web.admin.settings.signIn.provider.loginName.providerName")
            }
            description={
              mode === "oauth"
                ? t("web.admin.settings.signIn.provider.loginName.oauthDescription")
                : t("web.admin.settings.signIn.provider.loginName.description")
            }
          />
          {mode === "oauth" ? (
            <SettingFieldRow label={t("web.admin.settings.signIn.provider.loginName.preview")}>
              <span
                aria-hidden="true"
                className="border-border bg-muted/30 inline-flex items-center gap-2 rounded-lg border px-4 py-2 text-sm font-medium"
              >
                <KeyRound className="size-4" />
                {t("web.admin.settings.signIn.provider.loginName.previewButton", { name: shown })}
              </span>
              <span className="sr-only">
                {t("web.admin.settings.signIn.provider.loginName.previewSr", { name: shown })}
              </span>
            </SettingFieldRow>
          ) : null}
        </>
      ),
    });
  }

  let blockedReason: ReactNode = null;
  if (!enabled) {
    if (!installation.enabled) {
      blockedReason = t("web.admin.settings.signIn.provider.blocked.pluginOff", {
        link: (chunks) => (
          <Link
            to={pluginPagePath(installation.plugin_id)}
            className="text-foreground underline underline-offset-4"
          >
            {chunks}
          </Link>
        ),
      });
    } else if (otherActive !== null) {
      blockedReason = t("web.admin.settings.signIn.provider.blocked.otherActive", {
        active: otherActive,
        provider: title,
      });
    } else if (unsaved > 0) {
      blockedReason = t("web.admin.settings.signIn.provider.blocked.unsaved");
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
                <span aria-hidden="true">{SEPARATOR}</span>
                <span>
                  {mode === "oauth"
                    ? t("web.admin.settings.signIn.provider.header.oauthShown", { name: label })
                    : t("web.admin.settings.signIn.provider.header.shownAs", { name: label })}
                </span>
              </>
            ) : null}
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Button asChild size="sm" variant="ghost">
            <Link to={pluginPagePath(installation.plugin_id)}>
              {t("web.admin.settings.signIn.provider.header.pluginPage")}
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
              {t("web.admin.settings.signIn.provider.header.turnOff")}
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
              {t("web.admin.settings.signIn.provider.header.turnOn")}
            </Button>
          )}
        </div>
      </div>

      <FeedbackLine feedback={feedback} focusRef={statusRef} />
      {blockedReason ? <p className="text-muted-foreground pt-1 text-sm">{blockedReason}</p> : null}
      {missing.length > 0 ? (
        <p className="pt-1 text-sm text-amber-600 dark:text-amber-400">
          {t(
            enabled
              ? "web.admin.settings.signIn.provider.missing.enabled"
              : "web.admin.settings.signIn.provider.missing.disabled",
            {
              fields: missing.join(", "),
            },
          )}
        </p>
      ) : null}

      {steps.map((step, index) => (
        <Step key={step.key} number={index + 1} title={step.title} description={step.description}>
          {step.body}
        </Step>
      ))}

      {layout.advanced.length > 0 ? (
        <div className="pt-6">
          <h4 className="text-sm font-semibold">
            {t("web.admin.settings.signIn.provider.advanced.title")}
          </h4>
          <p className="text-muted-foreground text-xs">
            {t("web.admin.settings.signIn.provider.advanced.description")}
          </p>
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
          {t("web.admin.settings.signIn.provider.unsupported", {
            link: (chunks) => (
              <Link
                to={pluginPagePath(installation.plugin_id)}
                className="text-foreground underline underline-offset-4"
              >
                {chunks}
              </Link>
            ),
          })}
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
  const t = useT();
  if (!active) {
    return (
      <p className="text-muted-foreground pt-5 text-sm">
        {t("web.admin.settings.signIn.provider.none.description")}
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
  const t = useT();
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
          {t("web.admin.settings.signIn.provider.none.activeOn", { name: label })}
        </p>
        <Button
          type="button"
          size="sm"
          variant="outline"
          disabled={bindingSwitch.pending}
          onClick={bindingSwitch.askOff}
        >
          {t("web.admin.settings.signIn.provider.none.turnOffActive", { name: label })}
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
  const t = useT();
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
        {t("web.admin.settings.signIn.provider.slot.readFailed")}
      </p>
    );
  }

  const candidates = primarySignInInstallations(installations);
  if (candidates.length === 0) {
    return (
      <p className="text-muted-foreground py-3.5 text-sm">
        {t("web.admin.settings.signIn.provider.slot.noPlugins", {
          b: (chunks) => <span className="text-foreground font-medium">{chunks}</span>,
          link: (chunks) => (
            <Link to={CATALOG_PATH} className="text-foreground underline underline-offset-4">
              {chunks}
            </Link>
          ),
        })}
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
        state: providerState(installation, missingSignInSetup(layout), t),
      };
    }),
    {
      id: "none",
      title: t("web.admin.settings.signIn.provider.slot.noneTitle"),
      detail: t("web.admin.settings.signIn.provider.slot.noneDetail"),
    },
  ];

  return (
    <div className="pb-2">
      <span id={groupLabelId} className="sr-only">
        {t("web.admin.settings.signIn.provider.slot.groupLabel")}
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
                "bg-muted/20 hover:bg-muted/40 rounded-xl border px-3.5 py-3 text-start transition-colors",
                on
                  ? "border-[var(--settings-accent)] ring-1 ring-[var(--settings-accent)]"
                  : "border-border",
              )}
            >
              <span className="flex items-center justify-between gap-2">
                <span className="text-sm font-semibold">{choice.title}</span>
                {choice.state ? (
                  <span className="text-muted-foreground border-border inline-flex shrink-0 items-center gap-1.5 rounded-full border px-2 py-0.5 text-xs">
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
          {t("web.admin.settings.signIn.provider.slot.oneAtATime", {
            name: providerTitle(active, candidates),
          })}
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
