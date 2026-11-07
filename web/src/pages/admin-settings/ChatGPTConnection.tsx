import { useEffect, useEffectEvent, useId, useRef, useState } from "react";
import { toast } from "sonner";
import { useOptionalAuth } from "@/hooks/useAuth";
import { captureProfileRequestContext, isCapturedProfileAuthorityActive } from "@/api/client";
import type { ChatGPTSettingsChange } from "@/api/v2/adminSettingsSnapshot";
import {
  useChatGPTModels,
  useChatGPTStatus,
  useCompleteChatGPTLogin,
  useDisconnectChatGPTAccount,
  useSelectChatGPTAccount,
  useStartChatGPTLogin,
} from "@/hooks/queries/admin/chatgpt";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { useT } from "@/i18n";
import { SettingField, SettingFieldStatus } from "./SettingField";

type LoginAttempt = { id: string; confirmed: boolean };

/** The sign-in callback shown before the server names its own. */
const DEFAULT_CALLBACK_URI = "http://127.0.0.1:51121/auth/callback";

/** Which note a saved account carries in the account select. */
function accountState(account: {
  plan_ineligible?: boolean;
  needs_login?: boolean;
  usage_limited?: boolean;
  connected?: boolean;
}): "ineligible" | "signin" | "paused" | "ok" {
  if (account.plan_ineligible) return "ineligible";
  if (account.needs_login) return "signin";
  if (account.usage_limited) return "paused";
  if (!account.connected) return "signin";
  return "ok";
}

export function ChatGPTConnection(props: {
  model: string;
  onModelChange: (model: string) => void;
  onConnectionConfirmed?: () => void;
  onSettingsTransitionStarted?: () => (change: ChatGPTSettingsChange) => void;
}) {
  useOptionalAuth();
  const authority = captureProfileRequestContext();
  const key = JSON.stringify([
    authority?.authContextVersion,
    authority?.serverOrigin,
    authority?.profileId,
    authority?.profileTokenGeneration,
  ]);
  return <ChatGPTConnectionPanel key={key} {...props} />;
}

function ChatGPTConnectionPanel({
  model,
  onModelChange,
  onConnectionConfirmed,
  onSettingsTransitionStarted,
}: {
  model: string;
  onModelChange: (model: string) => void;
  onConnectionConfirmed?: () => void;
  onSettingsTransitionStarted?: () => (change: ChatGPTSettingsChange) => void;
}) {
  const t = useT();
  const authority = captureProfileRequestContext();
  const authorityActive = () => authority !== null && isCapturedProfileAuthorityActive(authority);
  const [loginOpen, setLoginOpen] = useState(false);
  const [welcomeOpen, setWelcomeOpen] = useState(false);
  const [callbackURL, setCallbackURL] = useState("");
  const [callbackError, setCallbackError] = useState("");
  const loginAttempt = useRef<LoginAttempt | null>(null);
  const callbackID = useId();
  const start = useStartChatGPTLogin();
  const complete = useCompleteChatGPTLogin();

  useEffect(
    () => () => {
      loginAttempt.current = null;
    },
    [],
  );

  function resetLoginDialog() {
    setLoginOpen(false);
    setCallbackURL("");
    setCallbackError("");
    start.reset();
    complete.reset();
  }

  function clearLogin() {
    loginAttempt.current = null;
    resetLoginDialog();
  }

  function confirmLogin(attempt: LoginAttempt, firstConnection: boolean) {
    if (!authorityActive() || loginAttempt.current !== attempt || attempt.confirmed) return;
    attempt.confirmed = true;
    resetLoginDialog();
    onConnectionConfirmed?.();
    toast.success(t("web.admin.settings.ai.chatgptConnection.connectedToast"));
    if (firstConnection) setWelcomeOpen(true);
  }

  const status = useChatGPTStatus(loginOpen);
  const observeLogin = useEffectEvent(() => {
    const next = status.data;
    const attempt = start.data;
    const local = loginAttempt.current;
    if (
      !authorityActive() ||
      !loginOpen ||
      !next ||
      !attempt ||
      !local ||
      local.id !== attempt.attempt_id ||
      local.confirmed
    )
      return;
    if (next.attempt_id === attempt.attempt_id && next.login_result === "connected") {
      confirmLogin(local, next.login_first_connection === true);
      return;
    }
    if (complete.isPending || status.isFetching) return;
    if (next.attempt_id === attempt.attempt_id && next.login_pending) return;
    const expired = Date.now() >= Date.parse(attempt.expires_at);
    if (!expired && next.attempt_id !== attempt.attempt_id) return;
    clearLogin();
    toast.error(t("web.admin.settings.ai.chatgptConnection.signInIncomplete"));
  });
  useEffect(() => {
    observeLogin();
  }, [status.dataUpdatedAt, status.isFetching, loginOpen, complete.isPending]);
  const select = useSelectChatGPTAccount();
  const disconnect = useDisconnectChatGPTAccount();
  const active = status.data?.accounts.find(
    (account) => account.client_id === status.data.active_client_id,
  );
  const connected = active?.connected === true;
  const authenticated =
    !!active &&
    !active.plan_ineligible &&
    !active.needs_login &&
    (active.connected || active.usage_limited === true);
  const models = useChatGPTModels(active?.client_id ?? "", authenticated);

  async function begin(clientID?: string) {
    const attempt: LoginAttempt = { id: "", confirmed: false };
    loginAttempt.current = attempt;
    setCallbackURL("");
    setCallbackError("");
    try {
      const login = await start.mutateAsync(clientID);
      if (!authorityActive() || loginAttempt.current !== attempt) return;
      attempt.id = login.attempt_id;
      setLoginOpen(true);
    } catch {
      if (!authorityActive() || loginAttempt.current !== attempt) return;
      loginAttempt.current = null;
      toast.error(t("web.admin.settings.ai.chatgptConnection.startFailed"));
    }
  }

  async function finish() {
    const attempt = loginAttempt.current;
    if (
      !authorityActive() ||
      !start.data ||
      complete.isPending ||
      !attempt ||
      attempt.confirmed ||
      attempt.id !== start.data.attempt_id
    )
      return;
    let pasted: URL;
    try {
      pasted = new URL(callbackURL.trim());
      const expected = new URL(start.data.callback_uri ?? "");
      if (
        pasted.origin !== expected.origin ||
        pasted.pathname !== expected.pathname ||
        pasted.username ||
        pasted.password ||
        pasted.hash ||
        !pasted.searchParams.get("state") ||
        (!pasted.searchParams.get("code") && !pasted.searchParams.get("error"))
      ) {
        throw new Error("Invalid callback URL");
      }
    } catch {
      setCallbackError(t("web.admin.settings.ai.chatgptConnection.callbackInvalid"));
      return;
    }
    setCallbackError("");
    try {
      const acceptTransition = onSettingsTransitionStarted?.();
      const change = await complete.mutateAsync(callbackURL.trim());
      if (!authorityActive() || loginAttempt.current !== attempt) return;
      acceptTransition?.(change);
      confirmLogin(attempt, change.firstConnection === true);
    } catch {
      if (!authorityActive() || loginAttempt.current !== attempt || attempt.confirmed) return;
      setCallbackError(t("web.admin.settings.ai.chatgptConnection.completeFailed"));
      void status.refetch();
    }
  }

  async function choose(clientID: string) {
    try {
      const acceptTransition = onSettingsTransitionStarted?.();
      const change = await select.mutateAsync(clientID);
      if (!authorityActive()) return;
      acceptTransition?.(change);
      if (change.firstConnection) setWelcomeOpen(true);
    } catch {
      if (!authorityActive()) return;
      toast.error(t("web.admin.settings.ai.chatgptConnection.switchFailed"));
    }
  }

  async function signOut(clientID: string) {
    if (!authorityActive()) return;
    try {
      const result = await disconnect.mutateAsync(clientID);
      if (!authorityActive()) return;
      if (result.revocation_confirmed)
        toast.success(t("web.admin.settings.ai.chatgptConnection.disconnected"));
      else toast.error(t("web.admin.settings.ai.chatgptConnection.disconnectedLocally"));
    } catch {
      if (!authorityActive()) return;
      toast.error(t("web.admin.settings.ai.chatgptConnection.disconnectFailed"));
    }
  }

  // The callback URL with its query elided; a URL in every language.
  const callbackPlaceholder = `${start.data?.callback_uri ?? DEFAULT_CALLBACK_URI}?…`;
  const savedAccounts = status.data?.accounts ?? [];
  const availableModels = models.data?.models ?? [];
  const selectedModelAvailable = availableModels.some((item) => item.id === model);

  return (
    <div className="space-y-3 py-3">
      <p className="text-muted-foreground text-xs">
        {t("web.admin.settings.ai.chatgptConnection.intro")}
      </p>
      {status.isError ? (
        <div className="flex items-center gap-3">
          <SettingFieldStatus tone="warn">
            {t("web.admin.settings.ai.chatgptConnection.loadFailed")}
          </SettingFieldStatus>
          <Button size="sm" variant="outline" onClick={() => void status.refetch()}>
            {t("common.retry")}
          </Button>
        </div>
      ) : null}
      {savedAccounts.length > 0 ? (
        <SettingField
          label={t("web.admin.settings.ai.chatgptConnection.account")}
          type="select"
          value={active?.client_id ?? "disconnected"}
          options={[
            {
              value: "disconnected",
              label: t("web.admin.settings.ai.chatgptConnection.chooseAccount"),
              disabled: true,
            },
            ...savedAccounts.map((account, index) => {
              return {
                value: account.client_id,
                label: t("web.admin.settings.ai.chatgptConnection.accountOption", {
                  state: accountState(account),
                  account:
                    account.email || t("web.admin.settings.ai.chatgptConnection.accountFallback"),
                  number: index + 1,
                }),
                disabled:
                  account.plan_ineligible === true ||
                  account.needs_login ||
                  (!account.connected && !account.usage_limited),
              };
            }),
          ]}
          disabled={select.isPending || disconnect.isPending}
          onChange={(value) => void choose(value)}
        />
      ) : null}
      {authenticated ? (
        <>
          {connected ? (
            <SettingFieldStatus>
              {t("web.admin.settings.ai.chatgptConnection.usingPlan", { email: active.email })}
            </SettingFieldStatus>
          ) : null}
          <SettingField
            label={t("web.admin.settings.ai.model.label")}
            type="select"
            value={selectedModelAvailable ? model : "choose-model"}
            options={[
              {
                value: "choose-model",
                label: t("web.admin.settings.ai.chatgptConnection.chooseModel"),
                disabled: true,
              },
              ...availableModels.map((item) => ({ value: item.id, label: item.name || item.id })),
            ]}
            onChange={onModelChange}
            disabled={models.isLoading || models.isError || availableModels.length === 0}
          />
          {models.isError ? (
            <div className="flex items-center gap-3">
              <SettingFieldStatus tone="warn">
                {t("web.admin.settings.ai.chatgptConnection.modelsFailed")}
              </SettingFieldStatus>
              <Button size="sm" variant="outline" onClick={() => void models.refetch()}>
                {t("web.admin.settings.ai.chatgptConnection.retryModels")}
              </Button>
            </div>
          ) : availableModels.length === 0 && !models.isLoading ? (
            <SettingFieldStatus tone="warn">
              {t("web.admin.settings.ai.chatgptConnection.noModels")}
            </SettingFieldStatus>
          ) : null}
        </>
      ) : null}
      <div className="flex flex-wrap items-center gap-2">
        <Button
          size="sm"
          onClick={() => void begin()}
          disabled={start.isPending || status.isLoading || status.isError}
        >
          {t("web.admin.settings.ai.chatgptConnection.continue")}
        </Button>
        {savedAccounts.map((account, index) => (
          <div key={account.client_id} className="flex flex-wrap items-center gap-2">
            {!account.plan_ineligible && (!account.usage_limited || account.needs_login) ? (
              <Button
                size="sm"
                variant="outline"
                disabled={start.isPending}
                onClick={() => void begin(account.client_id)}
              >
                {account.email
                  ? t("web.admin.settings.ai.chatgptConnection.reconnectEmail", {
                      email: account.email,
                    })
                  : t("web.admin.settings.ai.chatgptConnection.reconnectNumbered", {
                      number: index + 1,
                    })}
              </Button>
            ) : null}
            {account.client_id !== active?.client_id ? (
              <Button
                size="sm"
                variant="outline"
                disabled={disconnect.isPending}
                onClick={() => void signOut(account.client_id)}
              >
                {account.email
                  ? t("web.admin.settings.ai.chatgptConnection.disconnectEmail", {
                      email: account.email,
                    })
                  : t("web.admin.settings.ai.chatgptConnection.disconnectNumbered", {
                      number: index + 1,
                    })}
              </Button>
            ) : null}
            {account.plan_ineligible ? (
              <SettingFieldStatus tone="warn">
                {t("web.admin.settings.ai.chatgptConnection.planIneligible")}
              </SettingFieldStatus>
            ) : account.usage_limited ? (
              <SettingFieldStatus tone="warn">
                {t("web.admin.settings.ai.chatgptConnection.usagePaused")}
              </SettingFieldStatus>
            ) : null}
          </div>
        ))}
        {active ? (
          <Button
            size="sm"
            variant="outline"
            disabled={disconnect.isPending}
            onClick={() => void signOut(active.client_id)}
          >
            {t("web.admin.settings.ai.chatgptConnection.disconnect")}
          </Button>
        ) : null}
        <a
          className="text-primary text-xs underline"
          href="https://chatgpt.com/settings/usage"
          target="_blank"
          rel="noreferrer"
        >
          {t("web.admin.settings.ai.chatgptConnection.manageUsage")}
        </a>
      </div>
      <Dialog
        open={loginOpen}
        onOpenChange={(open) => {
          if (!open && !complete.isPending) clearLogin();
        }}
      >
        <DialogContent
          className="max-h-[calc(100dvh-2rem)] overflow-y-auto [&>*]:min-w-0"
          showCloseButton={!complete.isPending}
        >
          <DialogHeader>
            <DialogTitle>{t("web.admin.settings.ai.chatgptConnection.dialogTitle")}</DialogTitle>
            <DialogDescription>
              {t("web.admin.settings.ai.chatgptConnection.dialogDescription")}
            </DialogDescription>
          </DialogHeader>
          <ol className="list-decimal space-y-3 ps-5 text-sm">
            <li>
              {t("web.admin.settings.ai.chatgptConnection.step1")}
              <div className="mt-2">
                <Button asChild size="sm">
                  <a href={start.data?.authorization_url} target="_blank" rel="noopener noreferrer">
                    {t("web.admin.settings.ai.chatgptConnection.openSignIn")}
                  </a>
                </Button>
              </div>
            </li>
            <li>{t("web.admin.settings.ai.chatgptConnection.step2")}</li>
            <li>{t("web.admin.settings.ai.chatgptConnection.step3")}</li>
          </ol>
          <form
            className="space-y-3"
            onSubmit={(event) => {
              event.preventDefault();
              void finish();
            }}
          >
            <Label htmlFor={callbackID}>
              {t("web.admin.settings.ai.chatgptConnection.browserUrl")}
            </Label>
            <Input
              id={callbackID}
              type="text"
              placeholder={callbackPlaceholder}
              value={callbackURL}
              onChange={(event) => {
                setCallbackURL(event.target.value);
                setCallbackError("");
              }}
              maxLength={32768}
              autoComplete="off"
              autoCapitalize="off"
              spellCheck={false}
              disabled={complete.isPending}
              aria-invalid={callbackError ? true : undefined}
              aria-describedby={callbackError ? `${callbackID}-error` : undefined}
            />
            {callbackError ? (
              <p id={`${callbackID}-error`} role="alert" className="text-destructive text-sm">
                {callbackError}
              </p>
            ) : null}
            <Button type="submit" disabled={!callbackURL.trim() || complete.isPending}>
              {complete.isPending
                ? t("web.admin.settings.ai.chatgptConnection.connecting")
                : t("web.admin.settings.ai.chatgptConnection.connect")}
            </Button>
          </form>
          <p className="text-muted-foreground text-xs">
            {t("web.admin.settings.ai.chatgptConnection.deadline")}
          </p>
          <Button variant="outline" disabled={complete.isPending} onClick={clearLogin}>
            {t("common.cancel")}
          </Button>
        </DialogContent>
      </Dialog>
      <Dialog open={welcomeOpen} onOpenChange={setWelcomeOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("web.admin.settings.ai.chatgptConnection.welcomeTitle")}</DialogTitle>
            <DialogDescription>
              {t("web.admin.settings.ai.chatgptConnection.welcomeDescription")}
            </DialogDescription>
          </DialogHeader>
          <Button onClick={() => setWelcomeOpen(false)}>
            {t("web.admin.settings.ai.chatgptConnection.gotIt")}
          </Button>
        </DialogContent>
      </Dialog>
    </div>
  );
}
