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
  authPluginInstallations,
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
export const RECHECK_INTERVAL_KEY = "auth.provider_recheck_interval";
export const OUTAGE_POLICY_KEY = "auth.provider_recheck_outage_policy";
const RECHECK_KEYS = [RECHECK_INTERVAL_KEY, OUTAGE_POLICY_KEY];
// Both only work for Silo-password accounts: the server refuses a signup while
// password sign-in is off, and a reset needs a local password to replace.
const PASSWORD_ACCOUNT_KEYS = ["signup.enabled", "password_reset.self_service_enabled"];
// Token lifetimes apply to every sign-in, provider or password alike.
const SESSION_KEYS = ["auth.access_token_expiry", "auth.refresh_token_expiry"];
const ACCOUNT_KEYS = [...SESSION_KEYS, EMAIL_MATCH_KEY, ...RECHECK_KEYS];
// server.public_url is read, not edited: OpenID Connect needs it.
const KEYS = [LOCAL_LOGIN_KEY, ...PASSWORD_ACCOUNT_KEYS, ...ACCOUNT_KEYS, "server.public_url"];

const RECHECK_INTERVALS = [
  { value: "15m", label: "15 minutes" },
  { value: "1h", label: "1 hour" },
  { value: "6h", label: "6 hours" },
  { value: "12h", label: "12 hours (default)" },
  { value: "1d", label: "1 day" },
  { value: "7d", label: "7 days" },
];

const OUTAGE_POLICIES = [
  { value: "fail_open", label: "Keep people signed in (default)" },
  { value: "fail_closed", label: "Stop sessions from renewing" },
];

// What each provider-only field holds when nobody has set it; an empty value
// reads as the same default.
const PROVIDER_FIELD_DEFAULTS: Record<string, string> = {
  [EMAIL_MATCH_KEY]: "false",
  [RECHECK_INTERVAL_KEY]: "12h",
  [OUTAGE_POLICY_KEY]: "fail_open",
};

/** The recheck interval options, plus a stored value none of them names. */
function intervalOptions(current: string) {
  const value = current.trim();
  if (!value || RECHECK_INTERVALS.some((option) => option.value === value)) {
    return RECHECK_INTERVALS;
  }
  return [...RECHECK_INTERVALS, { value, label: value }];
}

/**
 * Settings → Sign-in: the server's one external sign-in provider (OIDC or
 * LDAP) as guided setup, whether Silo passwords still sign in, public signups
 * and self-service password reset, how long anyone stays signed in, how first
 * sign-ins match accounts, and how often Silo asks the provider again. Every
 * edit, the provider's configuration included, saves through the save bar;
 * only turning a provider on or off applies at once. The rules are in
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
  // Email matching and the access re-check only act on accounts a sign-in
  // plugin signs in: matching needs OIDC or LDAP (a network sign-in never
  // matches by email), and the re-check also covers network sign-in. Without
  // one installed they are noise, but a field stays while the installations
  // are unknown or while it holds a value other than its default, saved or
  // staged, so nothing set is ever out of reach.
  const primaryInstalled = primarySignInInstallations(installations.data).length > 0;
  const anySignInInstalled = authPluginInstallations(installations.data).length > 0;

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
  const holdsSetting = (key: string) => {
    const fallback = PROVIDER_FIELD_DEFAULTS[key];
    const isSet = (value: string) => value.trim() !== "" && value.trim() !== fallback;
    return form.isDirty(key) || isSet(form.getValue(key)) || isSet(form.getPersistedValue(key));
  };
  const showEmailMatch = !providerKnown || primaryInstalled || holdsSetting(EMAIL_MATCH_KEY);
  const showRecheck = !providerKnown || anySignInInstalled || RECHECK_KEYS.some(holdsSetting);
  const providerVerbs = [showEmailMatch && "matched", showRecheck && "re-checked"].filter(Boolean);
  const sessionAdvancedKeys = [...SESSION_KEYS, ...(showRecheck ? RECHECK_KEYS : [])];

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
          label="Single sign-on"
          description="Let people sign in with your identity provider. A server uses one provider at a time."
          dirty={drafts.changeCount > 0}
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
        </FieldGroup>

        <NetworkSignInSection installations={installations.data} />

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
            settingKey={LOCAL_LOGIN_KEY}
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
            settingKey="signup.enabled"
            dirty={form.isDirty("signup.enabled")}
            type="toggle"
            description="Anyone with a valid invite code can create an account."
            value={form.getValue("signup.enabled")}
            onChange={(v) => form.setValue("signup.enabled", v)}
            restartRequired={restartKeys.has("signup.enabled")}
          />
          <SettingField
            label="Self-service password reset"
            settingKey="password_reset.self_service_enabled"
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
          label="Accounts and sessions"
          description={
            providerVerbs.length > 0
              ? `How long anyone stays signed in, and how a sign-in provider's accounts are ${providerVerbs.join(" and ")}.`
              : "How long anyone stays signed in."
          }
          dirty={ACCOUNT_KEYS.some((key) => form.isDirty(key))}
        >
          {showEmailMatch ? (
            <SettingField
              label="Match existing accounts by email"
              settingKey={EMAIL_MATCH_KEY}
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
          <AdvancedSection
            id="sign-in.sessions"
            count={sessionAdvancedKeys.length}
            forceOpen={sessionAdvancedKeys.some((key) => form.isDirty(key))}
          >
            <SettingField
              label="Access token expiry"
              settingKey="auth.access_token_expiry"
              dirty={form.isDirty("auth.access_token_expiry")}
              type="duration"
              description="How long before an app silently renews, e.g. 30m."
              value={form.getValue("auth.access_token_expiry")}
              onChange={(v) => form.setValue("auth.access_token_expiry", v)}
              restartRequired={restartKeys.has("auth.access_token_expiry")}
            />
            <SettingField
              label="Refresh token expiry"
              settingKey="auth.refresh_token_expiry"
              dirty={form.isDirty("auth.refresh_token_expiry")}
              type="duration"
              description="How long someone stays signed in, e.g. 30d. Also how long an account whose sign-in provider can't re-check it keeps its API keys and Audiobookshelf sessions without signing in through the provider."
              value={form.getValue("auth.refresh_token_expiry")}
              onChange={(v) => form.setValue("auth.refresh_token_expiry", v)}
              restartRequired={restartKeys.has("auth.refresh_token_expiry")}
            />
            {showRecheck ? (
              <>
                <SettingField
                  label="Re-check access every"
                  settingKey={RECHECK_INTERVAL_KEY}
                  dirty={form.isDirty(RECHECK_INTERVAL_KEY)}
                  type="select"
                  description="Silo asks the provider again whether each person may still sign in, so someone removed there is signed out of Silo at the next check."
                  value={form.getValue(RECHECK_INTERVAL_KEY) || "12h"}
                  onChange={(value) => form.setValue(RECHECK_INTERVAL_KEY, value)}
                  options={intervalOptions(form.getValue(RECHECK_INTERVAL_KEY))}
                />
                <SettingField
                  label="If the provider is unreachable"
                  settingKey={OUTAGE_POLICY_KEY}
                  dirty={form.isDirty(OUTAGE_POLICY_KEY)}
                  type="select"
                  description={
                    form.getValue(OUTAGE_POLICY_KEY) === "fail_closed"
                      ? "A session due for a check can't renew until the provider answers, so apps stop working during an outage and carry on once it's over."
                      : "Sessions keep renewing and Silo asks again at the next renewal, so an outage doesn't sign anyone out."
                  }
                  value={form.getValue(OUTAGE_POLICY_KEY) || "fail_open"}
                  onChange={(value) => form.setValue(OUTAGE_POLICY_KEY, value)}
                  options={OUTAGE_POLICIES}
                />
              </>
            ) : null}
          </AdvancedSection>
        </FieldGroup>
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
