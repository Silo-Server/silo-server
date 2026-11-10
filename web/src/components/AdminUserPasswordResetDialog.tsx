import { useId, useRef, useState, type ReactNode } from "react";
import { ChoiceOption } from "@/components/ChoiceOption";
import { ArrowRight, Copy } from "lucide-react";
import { Link } from "react-router";
import { toast } from "sonner";
import type { ProfileRequestContextSnapshot } from "@/api/client";
import type { AdminUser } from "@/api/types";
import { getAdminUser, type AdminPasswordReset } from "@/api/v2/adminUsers";
import { V2ProblemError } from "@/api/v2/request";
import { useIssuePasswordReset, useUpdateUser } from "@/hooks/queries/admin/users";
import { useT } from "@/i18n";
import { copyTextToClipboard } from "@/lib/clipboard";
import { formatDateTime } from "@/lib/datetime";
import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";

type ResetMethod = "email" | "link" | "password";

/** The server's bounds for a password (auth.ValidateNewPassword). */
const MIN_PASSWORD_CHARS = 8;
const MAX_PASSWORD_BYTES = 72;

/** Counts characters (code points) and UTF-8 bytes the way the server does. */
function passwordFits(password: string): boolean {
  return (
    [...password].length >= MIN_PASSWORD_CHARS &&
    new TextEncoder().encode(password).length <= MAX_PASSWORD_BYTES
  );
}

/**
 * Helps a locked-out account back in three ways: email it a single-use reset
 * link, create one to share, or set a temporary password now. Every option
 * signs the account out on all its devices.
 */
export function AdminUserPasswordResetDialog({
  user,
  emailAvailable,
  linkAvailable,
  profileContext,
  onClose,
}: {
  user: AdminUser;
  emailAvailable: boolean;
  linkAvailable: boolean;
  /** The authority the page reads the account under; a temporary password saves with it. */
  profileContext: ProfileRequestContextSnapshot;
  onClose: () => void;
}) {
  const t = useT();
  const issue = useIssuePasswordReset();
  const updateUser = useUpdateUser();
  const busy = useRef(false);
  const [pending, setPending] = useState(false);
  const [result, setResult] = useState<AdminPasswordReset | null>(null);
  const [passwordSet, setPasswordSet] = useState<{ mustChange: boolean } | null>(null);
  const [error, setError] = useState("");
  // The server refuses reset links for a disabled account; a temporary password still works.
  const canEmail = user.enabled && emailAvailable && user.email !== "";
  const canLink = user.enabled && linkAvailable;
  const [method, setMethod] = useState<ResetMethod>(
    canEmail ? "email" : canLink ? "link" : "password",
  );
  const [password, setPassword] = useState("");
  const [mustChange, setMustChange] = useState(true);
  const groupName = useId();
  const passwordId = useId();
  const mustChangeId = useId();

  async function send(delivery: "email" | "link") {
    if (busy.current) return;
    busy.current = true;
    setPending(true);
    setError("");
    try {
      setResult(await issue.mutateAsync({ id: user.id, delivery }));
    } catch (err) {
      setError(
        err instanceof Error ? err.message : t("web.admin.users.passwordResetDialog.issueFailed"),
      );
    } finally {
      busy.current = false;
      setPending(false);
    }
  }

  async function setTemporaryPassword() {
    if (busy.current) return;
    busy.current = true;
    setPending(true);
    setError("");
    try {
      // Read the account fresh so the save carries its current validator.
      const editor = await getAdminUser(user.id, profileContext);
      await updateUser.mutateAsync({
        editor,
        body: { password, require_password_change: mustChange ? true : undefined },
      });
      setPassword("");
      setPasswordSet({ mustChange });
    } catch (err) {
      if (err instanceof V2ProblemError && err.status === 412) {
        setError(t("web.admin.users.page.accountChanged"));
      } else {
        setError(
          err instanceof Error ? err.message : t("web.admin.users.passwordResetDialog.setFailed"),
        );
      }
    } finally {
      busy.current = false;
      setPending(false);
    }
  }

  async function copy(url: string) {
    try {
      await copyTextToClipboard(url);
      toast.success(t("web.admin.users.passwordResetDialog.linkCopied"));
    } catch {
      setError(t("web.admin.users.passwordResetDialog.copyFailed"));
    }
  }

  const expires = result ? formatDateTime(result.expires_at) : "";
  // An unconfirmed email leaves the admin to retry or switch to a link.
  const unconfirmed = result?.delivery === "email" && result.delivery_status !== "sent";
  const done = passwordSet !== null || (result !== null && !unconfirmed);
  const resetURL = result?.reset_url;
  const passwordValid = passwordFits(password);
  const copyLabel = t("web.admin.users.passwordResetDialog.copyLink");

  const primary = {
    email: {
      label: t("web.admin.users.passwordResetDialog.emailAction"),
      disabled: !canEmail,
      run: () => void send("email"),
    },
    link: {
      label: t("web.admin.users.passwordResetDialog.linkAction"),
      disabled: !canLink,
      run: () => void send("link"),
    },
    password: {
      label: t("web.admin.users.passwordResetDialog.passwordAction"),
      disabled: !passwordValid,
      run: () => void setTemporaryPassword(),
    },
  }[method];

  function emailHint(): string {
    if (canEmail) return t("web.admin.users.passwordResetDialog.emailHint", { email: user.email });
    if (!user.enabled) return t("web.admin.users.passwordResetDialog.emailHintDisabled");
    if (user.email === "") return t("web.admin.users.passwordResetDialog.emailHintNoAddress");
    return t("web.admin.users.passwordResetDialog.emailHintNoMail");
  }

  function linkHint(): ReactNode {
    if (canLink) return t("web.admin.users.passwordResetDialog.linkHint");
    if (!user.enabled) return t("web.admin.users.passwordResetDialog.linkHintDisabled");
    return t("web.admin.users.passwordResetDialog.linkHintNoUrl", {
      link: (chunks) => (
        <Link
          to="/admin/settings/general"
          className="text-foreground inline-flex items-center gap-1 font-medium hover:underline"
        >
          {chunks}
          <ArrowRight className="h-3 w-3" aria-hidden="true" />
        </Link>
      ),
    });
  }

  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !busy.current) onClose();
      }}
    >
      <DialogContent className="min-w-0 sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>
            {t("web.admin.users.passwordResetDialog.title", { username: user.username })}
          </DialogTitle>
          <DialogDescription>
            {t("web.admin.users.passwordResetDialog.description")}
          </DialogDescription>
        </DialogHeader>

        {error && (
          <p role="alert" className="text-destructive text-sm">
            {error}
          </p>
        )}

        {passwordSet ? (
          <p role="status" className="text-sm">
            {passwordSet.mustChange
              ? t("web.admin.users.passwordResetDialog.passwordSetMustChange", {
                  username: user.username,
                })
              : t("web.admin.users.passwordResetDialog.passwordSet", { username: user.username })}
          </p>
        ) : null}
        {result?.delivery === "email" && result.delivery_status === "sent" && (
          <p role="status" className="text-sm">
            {t("web.admin.users.passwordResetDialog.emailSent", { email: user.email, expires })}
          </p>
        )}
        {unconfirmed && (
          <p role="alert" className="text-sm">
            {t("web.admin.users.passwordResetDialog.emailUnconfirmed")}
          </p>
        )}
        {resetURL && (
          <div className="min-w-0 space-y-2">
            <div className="bg-muted min-w-0 overflow-hidden rounded-md p-2.5">
              <code className="block truncate text-xs">{resetURL}</code>
            </div>
            <p className="text-muted-foreground text-xs">
              {t("web.admin.users.passwordResetDialog.linkShare", {
                username: user.username,
                expires,
              })}
            </p>
            <Button variant="outline" size="sm" onClick={() => void copy(resetURL)}>
              <Copy className="me-1.5 h-4 w-4" /> {copyLabel}
            </Button>
          </div>
        )}

        {!done ? (
          <div
            role="radiogroup"
            aria-label={t("web.admin.users.passwordResetDialog.methodGroup")}
            className="space-y-2"
          >
            <ChoiceOption
              name={groupName}
              value="email"
              selected={method === "email"}
              disabled={!canEmail || pending}
              title={t("web.admin.users.passwordResetDialog.emailOption")}
              onSelect={setMethod}
            >
              {emailHint()}
            </ChoiceOption>
            <ChoiceOption
              name={groupName}
              value="link"
              selected={method === "link"}
              disabled={!canLink || pending}
              title={t("web.admin.users.passwordResetDialog.linkOption")}
              onSelect={setMethod}
            >
              {linkHint()}
            </ChoiceOption>
            <ChoiceOption
              name={groupName}
              value="password"
              selected={method === "password"}
              disabled={pending}
              title={t("web.admin.users.passwordResetDialog.passwordOption")}
              onSelect={setMethod}
            >
              <span>{t("web.admin.users.passwordResetDialog.passwordOptionHint")}</span>
              <div
                className={cn(
                  "mt-2.5 space-y-2",
                  method !== "password" && "pointer-events-none opacity-50",
                )}
              >
                <label htmlFor={passwordId} className="sr-only">
                  {t("web.admin.users.passwordResetDialog.passwordLabel")}
                </label>
                <Input
                  id={passwordId}
                  type="password"
                  autoComplete="new-password"
                  value={password}
                  disabled={method !== "password" || pending}
                  onChange={(event) => setPassword(event.target.value)}
                />
                <p className="text-muted-foreground text-xs">
                  {t("web.admin.users.passwordResetDialog.passwordRules", {
                    min: String(MIN_PASSWORD_CHARS),
                    max: String(MAX_PASSWORD_BYTES),
                  })}
                </p>
                <div className="flex items-center gap-2">
                  <input
                    id={mustChangeId}
                    type="checkbox"
                    className="accent-amber-500"
                    checked={mustChange}
                    disabled={method !== "password" || pending}
                    onChange={(event) => setMustChange(event.target.checked)}
                  />
                  <label htmlFor={mustChangeId} className="text-foreground text-xs">
                    {t("web.admin.users.passwordResetDialog.mustChange")}
                  </label>
                </div>
              </div>
            </ChoiceOption>
          </div>
        ) : null}

        <DialogFooter>
          {done ? (
            <Button onClick={onClose}>{t("common.done")}</Button>
          ) : (
            <>
              <Button variant="ghost" disabled={pending} onClick={onClose}>
                {t("common.cancel")}
              </Button>
              <Button disabled={primary.disabled || pending} onClick={primary.run}>
                {primary.label}
              </Button>
            </>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
