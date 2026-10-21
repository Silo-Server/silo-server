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
import { useRestartKeys } from "@/hooks/useRestartKeys";
import { useSettingsForm } from "@/hooks/useSettingsForm";
import {
  activeSignInInstallation,
  BREAK_GLASS_REQUIRED_TEXT,
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
export default function SignInSettings() {
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
      showLocalError(
        "Password sign-in can't be turned off yet: no admin is a break-glass account. Open an admin on the Users page and turn on Break-glass account first, so someone can still sign in if the provider is down.",
      );
      return;
    }
    if (value === "false" && providerKnown && !providerOn) {
      showLocalError(
        "Turn on a sign-in provider first. With password sign-in off and no provider on, only break-glass admins could sign in.",
      );
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
        showLocalError(BREAK_GLASS_REQUIRED_TEXT);
      }
      // Every other failure is already reported by the save's own toast.
    }
  }

  if (form.isLoading) {
    return (
      <div className="space-y-6" role="status" aria-label="Loading settings">
        <Skeleton className="h-8 w-48" />
        <Skeleton className="h-24 w-full" />
        <Skeleton className="h-24 w-full" />
        <span className="sr-only">Loading settings</span>
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
      <SettingsPageHeader title="Sign-in" />

      <div className="flex-1 space-y-5">
        {capabilities.isError || (caps && !supported) ? (
          <p className="text-muted-foreground text-sm" role="status">
            External sign-in isn't available on this server, so only Silo passwords sign in.
          </p>
        ) : null}

        <FieldGroup
          label="Silo passwords"
          dirty={[LOCAL_LOGIN_KEY, ...PASSWORD_ACCOUNT_KEYS].some((key) => form.isDirty(key))}
          actions={
            <Link
              to="/admin/users?tab=invite-codes"
              className="text-muted-foreground hover:text-foreground inline-flex items-center gap-1.5 text-xs font-medium transition-colors"
            >
              Manage invite codes
              <ArrowRight className="h-3 w-3" aria-hidden="true" />
            </Link>
          }
        >
          <SettingField
            label="Allow password sign-in"
            dirty={form.isDirty(LOCAL_LOGIN_KEY)}
            type="toggle"
            description="Off: everyone signs in with the provider, except break-glass admins. Needs at least one break-glass admin; the server owner is one by default."
            value={localLoginOn ? "true" : "false"}
            onChange={changeLocalLogin}
            status={
              !breakGlassKnown ? null : breakGlass.length > 0 ? (
                <SettingFieldStatus tone="muted">
                  Break-glass {breakGlass.length === 1 ? "admin" : "admins"}:{" "}
                  {breakGlass.map((user) => user.username).join(", ")}
                </SettingFieldStatus>
              ) : (
                <SettingFieldStatus tone="warn">
                  No break-glass admin yet. Turn on Break-glass account on an admin's{" "}
                  <Link to="/admin/users" className="underline underline-offset-4">
                    user page
                  </Link>
                  .
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
            label="Public signups"
            dirty={form.isDirty("signup.enabled")}
            type="toggle"
            description="Anyone with a valid invite code can create an account."
            value={form.getValue("signup.enabled")}
            onChange={(v) => form.setValue("signup.enabled", v)}
            restartRequired={restartKeys.has("signup.enabled")}
          />
          <SettingField
            label="Self-service password reset"
            dirty={form.isDirty("password_reset.self_service_enabled")}
            type="toggle"
            description="Adds “Forgot password?” to the sign-in page, so people can get a reset link by email. Needs email and the public URL."
            value={form.getValue("password_reset.self_service_enabled")}
            onChange={(v) => form.setValue("password_reset.self_service_enabled", v)}
            restartRequired={restartKeys.has("password_reset.self_service_enabled")}
          />
          <AdvancedSection id="sign-in.locked-out" title="Locked out?">
            <p className="settings-field-note text-muted-foreground py-2 text-xs leading-relaxed">
              <code className="font-mono">/login?local=1</code> shows the password form for
              break-glass admins, and{" "}
              <code className="font-mono">silo auth local-login enable</code> run on the server
              turns password sign-in back on.
            </p>
          </AdvancedSection>
        </FieldGroup>

        <FieldGroup
          label="Single sign-on"
          description="Let people sign in with your identity provider. A server uses one provider at a time."
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
              label="Match existing accounts by email"
              dirty={form.isDirty(EMAIL_MATCH_KEY)}
              type="toggle"
              description="A first sign-in connects to the regular Silo account with the same email, when the provider says the email is verified. Admin, owner and break-glass accounts are never matched."
              value={emailMatchOn ? "true" : "false"}
              onChange={(value) => form.setValue(EMAIL_MATCH_KEY, value)}
              status={
                <SettingFieldStatus tone={emailMatchOn ? "warn" : "muted"}>
                  Whoever controls that email at the provider takes over the Silo account. Silo
                  doesn't verify account emails, so the account matched is whichever one registered
                  or set the address first; matching signs it out everywhere and deletes its API
                  keys. Turn this on only if the provider verifies every email and people can't
                  change their own.
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
