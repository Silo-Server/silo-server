import { useMemo, useRef, useState } from "react";
import { useIsMutating } from "@tanstack/react-query";
import { ArrowRight } from "lucide-react";
import { Link } from "react-router";

import { V2ProblemError } from "@/api/v2/request";
import { FeedbackLine, type Feedback } from "@/components/admin/FeedbackLine";
import { AdvancedSection } from "@/components/settings/AdvancedSection";
import { SettingsPageHeader } from "@/components/settings/SettingsPageHeader";
import { Skeleton } from "@/components/ui/skeleton";
import { useAdminPluginInstallations } from "@/hooks/queries/admin/plugins";
import {
  signInBindingMutationKey,
  useExternalSignInCapabilities,
} from "@/hooks/queries/admin/externalSignIn";
import { useAdminUsers } from "@/hooks/queries/admin/users";
import { useT } from "@/i18n";
import { useRestartKeys } from "@/hooks/useRestartKeys";
import { useSettingsForm } from "@/hooks/useSettingsForm";
import {
  activeSignInInstallation,
  breakGlassRequiredText,
  primarySignInInstallations,
} from "@/lib/externalSignInAdmin";
import { SaveBar } from "@/components/SaveBar";

import { FieldGroup } from "./FieldGroup";
import { SettingField, SettingFieldStatus } from "./SettingField";
import { NetworkSignInSection } from "./NetworkSignInSection";
import { SignInProviderSlot } from "./SignInProviderSlot";
import { useSignInProviderDrafts } from "./useSignInProviderDrafts";

export const LOCAL_LOGIN_KEY = "auth.local_password_login";
export const EMAIL_MATCH_KEY = "auth.email_auto_match";
// Both only work for Silo-password accounts: the server refuses a signup while
// password sign-in is off, and a reset needs a local password to replace.
const PASSWORD_ACCOUNT_KEYS = ["signup.enabled", "password_reset.self_service_enabled"];
// Hidden tier: still saved and readable through the settings API, deliberately
// without a control here because the defaults suit every deployment we
// support — auth.access_token_expiry, auth.refresh_token_expiry,
// auth.provider_recheck_interval, auth.provider_recheck_outage_policy.
// server.public_url is read, not edited: OpenID Connect needs it.
const KEYS = [LOCAL_LOGIN_KEY, ...PASSWORD_ACCOUNT_KEYS, EMAIL_MATCH_KEY, "server.public_url"];

/**
 * Settings → Sign-in: whether Silo passwords still sign in, with public
 * signups and self-service password reset; the server's one external sign-in
 * provider (OIDC or LDAP) as guided setup, with how its first sign-ins match
 * existing accounts; and network sign-in. Every edit, the provider's
 * configuration included, saves through the save bar; only turning a provider
 * on or off applies at once. The rules are in
 * docs/architecture/external-sign-in.md.
 */
// Code shown in the Locked out? note: a path and a server command.
const LOCAL_LOGIN_PATH = "/login?local=1";
const LOCAL_LOGIN_COMMAND = "silo auth local-login enable";

export default function SignInSettings() {
  const t = useT();
  const form = useSettingsForm({ keys: useMemo(() => KEYS, []) });
  const restartKeys = useRestartKeys();
  const capabilities = useExternalSignInCapabilities();
  const installations = useAdminPluginInstallations();
  const users = useAdminUsers();
  const drafts = useSignInProviderDrafts(installations.data);
  // Turning a provider on or off writes the same binding row a save may write.
  const bindingWrites = useIsMutating({ mutationKey: signInBindingMutationKey });
  const [localError, setLocalError] = useState<string | null>(null);
  const localErrorRef = useRef<HTMLParagraphElement>(null);
  const [providerFeedback, setProviderFeedback] = useState<Feedback>(null);
  const providerFeedbackRef = useRef<HTMLParagraphElement>(null);

  const breakGlass = useMemo(
    () =>
      (users.data ?? []).filter(
        (user) => user.role === "admin" && user.enabled && user.break_glass && user.password_login,
      ),
    [users.data],
  );
  const breakGlassKnown = users.data !== undefined;
  // With passwords off, the provider that is on is how everyone else signs in.
  const providerOn = useMemo(() => {
    const active = activeSignInInstallation(installations.data);
    return active?.enabled === true;
  }, [installations.data]);
  const providerKnown = installations.data !== undefined;
  // Email matching only acts on accounts OIDC or LDAP signs in (a network
  // sign-in never matches by email). Without one installed it is noise, but it
  // stays while the installations are unknown or while it is on, saved or
  // staged, so nothing set is ever out of reach.
  const primaryInstalled = primarySignInInstallations(installations.data).length > 0;

  function showLocalError(text: string) {
    setLocalError(text);
    requestAnimationFrame(() => localErrorRef.current?.focus());
  }

  function changeLocalLogin(value: string) {
    if (value === "false" && breakGlassKnown && breakGlass.length === 0) {
      showLocalError(t("web.admin.settings.signIn.page.noBreakGlassError"));
      return;
    }
    if (value === "false" && providerKnown && !providerOn) {
      showLocalError(t("web.admin.settings.signIn.page.noProviderError"));
      return;
    }
    setLocalError(null);
    form.setValue(LOCAL_LOGIN_KEY, value);
  }

  async function handleSave() {
    setLocalError(null);
    setProviderFeedback(null);
    // The provider first: turning password sign-in off may rely on it.
    const provider = await drafts.save();
    if (!provider.ok) {
      setProviderFeedback({ tone: "error", text: provider.text });
      requestAnimationFrame(() => providerFeedbackRef.current?.focus());
      return;
    }
    if (form.dirtyCount === 0) return;
    try {
      await form.save();
    } catch (error) {
      if (error instanceof V2ProblemError && error.problemType === "break_glass_required") {
        showLocalError(breakGlassRequiredText());
      }
      // Every other failure is already reported by the save's own toast.
    }
  }

  if (form.isLoading) {
    return (
      <div
        className="space-y-6"
        role="status"
        aria-label={t("web.admin.settings.signIn.page.loading")}
      >
        <Skeleton className="h-8 w-48" />
        <Skeleton className="h-24 w-full" />
        <Skeleton className="h-24 w-full" />
        <span className="sr-only">{t("web.admin.settings.signIn.page.loading")}</span>
      </div>
    );
  }

  const caps = capabilities.data;
  const supported = caps?.available === true;
  const localLoginOn = form.getValue(LOCAL_LOGIN_KEY) !== "false";
  const emailMatchOn = form.getValue(EMAIL_MATCH_KEY) === "true";
  const publicUrlSet = form.getPersistedValue("server.public_url").trim() !== "";
  const emailMatchSet =
    form.isDirty(EMAIL_MATCH_KEY) ||
    emailMatchOn ||
    form.getPersistedValue(EMAIL_MATCH_KEY).trim() === "true";
  const showEmailMatch = !providerKnown || primaryInstalled || emailMatchSet;

  return (
    <div className="flex h-full flex-col">
      <SettingsPageHeader title={t("web.admin.settings.signIn.page.title")} />

      <div className="flex-1 space-y-5">
        {capabilities.isError || (caps && !supported) ? (
          <p className="text-muted-foreground text-sm" role="status">
            {t("web.admin.settings.signIn.page.externalUnavailable")}
          </p>
        ) : null}

        <FieldGroup
          label={t("web.admin.settings.signIn.page.passwordsGroup")}
          dirty={[LOCAL_LOGIN_KEY, ...PASSWORD_ACCOUNT_KEYS].some((key) => form.isDirty(key))}
          actions={
            <Link
              to="/admin/users?tab=invite-codes"
              className="text-muted-foreground hover:text-foreground inline-flex items-center gap-1.5 text-xs font-medium transition-colors"
            >
              {t("web.admin.settings.signIn.page.manageInviteCodes")}
              <ArrowRight className="h-3 w-3" aria-hidden="true" />
            </Link>
          }
        >
          <SettingField
            label={t("web.admin.settings.signIn.page.allowPasswordLabel")}
            dirty={form.isDirty(LOCAL_LOGIN_KEY)}
            type="toggle"
            description={t("web.admin.settings.signIn.page.allowPasswordDescription")}
            value={localLoginOn ? "true" : "false"}
            onChange={changeLocalLogin}
            status={
              !breakGlassKnown ? null : breakGlass.length > 0 ? (
                <SettingFieldStatus tone="muted">
                  {t("web.admin.settings.signIn.page.breakGlassAdmins", {
                    count: breakGlass.length,
                    names: breakGlass.map((user) => user.username).join(", "),
                  })}
                </SettingFieldStatus>
              ) : (
                <SettingFieldStatus tone="warn">
                  {t("web.admin.settings.signIn.page.noBreakGlassStatus", {
                    link: (chunks) => (
                      <Link to="/admin/users" className="underline underline-offset-4">
                        {chunks}
                      </Link>
                    ),
                  })}
                </SettingFieldStatus>
              )
            }
          />
          {localError ? (
            <p
              ref={localErrorRef}
              tabIndex={-1}
              role="alert"
              className="text-destructive py-2 text-sm focus:outline-none"
            >
              {localError}
            </p>
          ) : null}
          <SettingField
            label={t("web.admin.settings.signIn.page.publicSignupsLabel")}
            dirty={form.isDirty("signup.enabled")}
            type="toggle"
            description={t("web.admin.settings.signIn.page.publicSignupsDescription")}
            value={form.getValue("signup.enabled")}
            onChange={(v) => form.setValue("signup.enabled", v)}
            restartRequired={restartKeys.has("signup.enabled")}
          />
          <SettingField
            label={t("web.admin.settings.signIn.page.passwordResetLabel")}
            dirty={form.isDirty("password_reset.self_service_enabled")}
            type="toggle"
            description={t("web.admin.settings.signIn.page.passwordResetDescription")}
            value={form.getValue("password_reset.self_service_enabled")}
            onChange={(v) => form.setValue("password_reset.self_service_enabled", v)}
            restartRequired={restartKeys.has("password_reset.self_service_enabled")}
          />
          <AdvancedSection
            id="sign-in.locked-out"
            title={t("web.admin.settings.signIn.page.lockedOutTitle")}
          >
            <p className="settings-field-note text-muted-foreground py-2 text-xs leading-relaxed">
              {t("web.admin.settings.signIn.page.lockedOutNote", {
                login_path: LOCAL_LOGIN_PATH,
                command: LOCAL_LOGIN_COMMAND,
                code: (chunks) => <code className="font-mono">{chunks}</code>,
              })}
            </p>
          </AdvancedSection>
        </FieldGroup>

        <FieldGroup
          label={t("web.admin.settings.signIn.page.ssoGroup")}
          description={t("web.admin.settings.signIn.page.ssoDescription")}
          dirty={drafts.changeCount > 0 || form.isDirty(EMAIL_MATCH_KEY)}
        >
          <div className="settings-field-note">
            <FeedbackLine feedback={providerFeedback} focusRef={providerFeedbackRef} />
            <SignInProviderSlot
              installations={installations.data}
              loading={installations.isLoading}
              failed={installations.isError}
              drafts={drafts}
              localLoginOn={form.getPersistedValue(LOCAL_LOGIN_KEY) !== "false"}
              publicUrlSet={publicUrlSet}
              connectionTestServed={caps?.connection_test === true}
            />
          </div>
          {showEmailMatch ? (
            <SettingField
              label={t("web.admin.settings.signIn.page.emailMatchLabel")}
              dirty={form.isDirty(EMAIL_MATCH_KEY)}
              type="toggle"
              description={t("web.admin.settings.signIn.page.emailMatchDescription")}
              value={emailMatchOn ? "true" : "false"}
              onChange={(value) => form.setValue(EMAIL_MATCH_KEY, value)}
              status={
                <SettingFieldStatus tone={emailMatchOn ? "warn" : "muted"}>
                  {t("web.admin.settings.signIn.page.emailMatchWarning")}
                </SettingFieldStatus>
              }
            />
          ) : null}
        </FieldGroup>

        <NetworkSignInSection installations={installations.data} />
      </div>

      <SaveBar
        dirtyCount={form.dirtyCount + drafts.changeCount}
        onSave={() => void handleSave()}
        onDiscard={() => {
          setLocalError(null);
          setProviderFeedback(null);
          drafts.discard();
          form.discard();
        }}
        isSaving={form.isSaving || drafts.saving}
        canSave={bindingWrites === 0}
      />
    </div>
  );
}
