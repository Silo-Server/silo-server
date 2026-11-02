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
import { SettingField, SettingFieldStatus } from "./SettingField";

type LoginAttempt = { id: string; confirmed: boolean };

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
    toast.success("ChatGPT connected. Review your text model settings.");
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
    toast.error("Sign-in did not complete. Continue with ChatGPT to try again.");
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
      toast.error("Could not start ChatGPT sign-in. Refresh the connection status and try again.");
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
      setCallbackError("Paste the full URL from the address bar after ChatGPT finishes sign-in.");
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
      setCallbackError(
        "Could not complete sign-in. Check the pasted URL. If this attempt expired, close this dialog and start again.",
      );
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
      toast.error("Could not switch ChatGPT accounts. Refresh the connection status.");
    }
  }

  async function signOut(clientID: string) {
    if (!authorityActive()) return;
    try {
      const result = await disconnect.mutateAsync(clientID);
      if (!authorityActive()) return;
      if (result.revocation_confirmed) toast.success("ChatGPT disconnected.");
      else
        toast.error(
          "Disconnected locally. Open ChatGPT usage settings to confirm the app is disconnected.",
        );
    } catch {
      if (!authorityActive()) return;
      toast.error(
        "Could not disconnect ChatGPT. Refresh the connection status before trying again.",
      );
    }
  }

  const savedAccounts = status.data?.accounts ?? [];
  const availableModels = models.data?.models ?? [];
  const selectedModelAvailable = availableModels.some((item) => item.id === model);

  return (
    <div className="space-y-3 py-3">
      <p className="text-muted-foreground text-xs">
        Use your ChatGPT plan for subtitle and description translation. Usage counts toward your
        plan limits.
      </p>
      {status.isError ? (
        <div className="flex items-center gap-3">
          <SettingFieldStatus tone="warn">
            Could not load the ChatGPT connection.
          </SettingFieldStatus>
          <Button size="sm" variant="outline" onClick={() => void status.refetch()}>
            Retry
          </Button>
        </div>
      ) : null}
      {savedAccounts.length > 0 ? (
        <SettingField
          label="ChatGPT account"
          type="select"
          value={active?.client_id ?? "disconnected"}
          options={[
            { value: "disconnected", label: "Choose a connected account", disabled: true },
            ...savedAccounts.map((account, index) => {
              let stateLabel = "";
              if (account.plan_ineligible) stateLabel = " (plan sharing unavailable)";
              else if (account.needs_login) stateLabel = " (sign-in required)";
              else if (account.usage_limited) stateLabel = " (usage paused)";
              else if (!account.connected) stateLabel = " (sign-in required)";
              return {
                value: account.client_id,
                label: `${account.email || "ChatGPT account"} · ${index + 1}${stateLabel}`,
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
            <SettingFieldStatus>Using ChatGPT plan · {active.email}</SettingFieldStatus>
          ) : null}
          <SettingField
            label="Model"
            type="select"
            value={selectedModelAvailable ? model : "choose-model"}
            options={[
              { value: "choose-model", label: "Choose a ChatGPT model", disabled: true },
              ...availableModels.map((item) => ({ value: item.id, label: item.name || item.id })),
            ]}
            onChange={onModelChange}
            disabled={models.isLoading || models.isError || availableModels.length === 0}
          />
          {models.isError ? (
            <div className="flex items-center gap-3">
              <SettingFieldStatus tone="warn">
                Could not load models. Reconnect if your sign-in expired.
              </SettingFieldStatus>
              <Button size="sm" variant="outline" onClick={() => void models.refetch()}>
                Retry models
              </Button>
            </div>
          ) : availableModels.length === 0 && !models.isLoading ? (
            <SettingFieldStatus tone="warn">
              No text models are available to this ChatGPT account.
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
          Continue with ChatGPT
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
                Reconnect {account.email || `account ${index + 1}`}
              </Button>
            ) : null}
            {account.client_id !== active?.client_id ? (
              <Button
                size="sm"
                variant="outline"
                disabled={disconnect.isPending}
                onClick={() => void signOut(account.client_id)}
              >
                Disconnect {account.email || `account ${index + 1}`}
              </Button>
            ) : null}
            {account.plan_ineligible ? (
              <SettingFieldStatus tone="warn">
                This account cannot share ChatGPT plan usage. Check ChatGPT usage settings.
              </SettingFieldStatus>
            ) : account.usage_limited ? (
              <SettingFieldStatus tone="warn">
                ChatGPT plan usage is paused. Check ChatGPT usage settings, then choose Test text
                model to resume.
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
            Disconnect
          </Button>
        ) : null}
        <a
          className="text-primary text-xs underline"
          href="https://chatgpt.com/settings/usage"
          target="_blank"
          rel="noreferrer"
        >
          Manage usage
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
            <DialogTitle>Continue with ChatGPT</DialogTitle>
            <DialogDescription>
              Sign in with ChatGPT in a new tab, then paste the final browser address here to
              connect your account.
            </DialogDescription>
          </DialogHeader>
          <ol className="list-decimal space-y-3 pl-5 text-sm">
            <li>
              Open ChatGPT and allow Silo to use your plan.
              <div className="mt-2">
                <Button asChild size="sm">
                  <a href={start.data?.authorization_url} target="_blank" rel="noopener noreferrer">
                    Open ChatGPT sign-in
                  </a>
                </Button>
              </div>
            </li>
            <li>
              After approval, the tab will go to a local address and may show “can’t connect.” This
              is expected. Copy the full URL from that tab’s address bar.
            </li>
            <li>Return here, paste the URL below, and choose Connect ChatGPT.</li>
          </ol>
          <form
            className="space-y-3"
            onSubmit={(event) => {
              event.preventDefault();
              void finish();
            }}
          >
            <Label htmlFor={callbackID}>Browser URL</Label>
            <Input
              id={callbackID}
              type="text"
              placeholder={`${start.data?.callback_uri ?? "http://127.0.0.1:51121/auth/callback"}?…`}
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
              {complete.isPending ? "Connecting…" : "Connect ChatGPT"}
            </Button>
          </form>
          <p className="text-muted-foreground text-xs">
            Finish within ten minutes. Silo stores the connected account’s credentials securely on
            your server.
          </p>
          <Button variant="outline" disabled={complete.isPending} onClick={clearLogin}>
            Cancel
          </Button>
        </DialogContent>
      </Dialog>
      <Dialog open={welcomeOpen} onOpenChange={setWelcomeOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>You’re using your ChatGPT plan</DialogTitle>
            <DialogDescription>
              Eligible text AI requests in Silo use your ChatGPT plan. You can manage Silo’s access
              and usage limits in ChatGPT settings.
            </DialogDescription>
          </DialogHeader>
          <Button onClick={() => setWelcomeOpen(false)}>Got it</Button>
        </DialogContent>
      </Dialog>
    </div>
  );
}
