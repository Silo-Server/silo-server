import { useId } from "react";
import { ArrowRight } from "lucide-react";
import { Link } from "react-router";

import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Skeleton } from "@/components/ui/skeleton";
import {
  useLocalPasswordOffAccounts,
  type LocalPasswordOffAccounts,
} from "@/hooks/queries/admin/externalSignIn";
import { useT, type MessageKey } from "@/i18n";

/** How many accounts of each group the dialog names before linking to Users. */
export const LOCAL_PASSWORD_OFF_SHOWN = 5;

type Account = LocalPasswordOffAccounts["items"][number];

const REASON_TEXT: Partial<Record<Account["reason"], MessageKey>> = {
  administrator: "web.admin.settings.signIn.passwordsOff.reason.administrator",
  no_email: "web.admin.settings.signIn.passwordsOff.reason.noEmail",
  email_match_off: "web.admin.settings.signIn.passwordsOff.reason.emailMatchOff",
};

interface LocalPasswordOffDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onConfirm: () => void;
  /** The provider people sign in with once passwords are off. */
  provider: string;
  /** Whether that provider creates an account at a first sign-in. */
  autoProvision: boolean;
  /** The page's email matching value, saved or staged. */
  emailMatch: boolean;
}

/**
 * Asks before password sign-in is turned off, naming who loses their way in:
 * the accounts email matching connects at their first provider sign-in, and
 * the ones nothing connects until an administrator links them
 * (listAdminLocalPasswordOffAccounts).
 */
export function LocalPasswordOffDialog({
  open,
  onOpenChange,
  onConfirm,
  provider,
  autoProvision,
  emailMatch,
}: LocalPasswordOffDialogProps) {
  const t = useT();
  const matched = useLocalPasswordOffAccounts(
    "email_match",
    emailMatch,
    LOCAL_PASSWORD_OFF_SHOWN,
    open,
  );
  const unmatched = useLocalPasswordOffAccounts(
    "admin_link",
    emailMatch,
    LOCAL_PASSWORD_OFF_SHOWN,
    open,
  );
  const loading = matched.isPending || unmatched.isPending;
  const failed = matched.isError || unmatched.isError;
  const totals = matched.data?.totals ?? unmatched.data?.totals;
  const nobody = totals !== undefined && totals.email_match + totals.admin_link === 0;

  return (
    <AlertDialog open={open} onOpenChange={onOpenChange}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{t("web.admin.settings.signIn.passwordsOff.title")}</AlertDialogTitle>
          <AlertDialogDescription>
            {t("web.admin.settings.signIn.passwordsOff.description", { provider })}
          </AlertDialogDescription>
        </AlertDialogHeader>
        <div className="grid gap-4 text-sm" aria-live="polite" aria-busy={loading}>
          {loading && open ? (
            <div role="status" className="grid gap-2">
              <span className="sr-only">{t("web.admin.settings.signIn.passwordsOff.loading")}</span>
              <Skeleton className="h-4 w-3/4" />
              <Skeleton className="h-4 w-1/2" />
            </div>
          ) : failed ? (
            <p role="alert" className="text-destructive">
              {t("web.admin.settings.signIn.passwordsOff.loadFailed")}
            </p>
          ) : nobody ? (
            <p>{t("web.admin.settings.signIn.passwordsOff.nobodyAffected")}</p>
          ) : (
            <>
              {totals && totals.email_match > 0 ? (
                <AccountGroup
                  heading={t("web.admin.settings.signIn.passwordsOff.matchHeading", {
                    count: totals.email_match,
                  })}
                  notes={[t("web.admin.settings.signIn.passwordsOff.matchExplain", { provider })]}
                  accounts={matched.data?.items ?? []}
                  total={totals.email_match}
                />
              ) : null}
              {totals && totals.admin_link > 0 ? (
                <AccountGroup
                  heading={t("web.admin.settings.signIn.passwordsOff.linkHeading", {
                    count: totals.admin_link,
                  })}
                  notes={[
                    t("web.admin.settings.signIn.passwordsOff.linkExplain", { provider }),
                    ...(autoProvision
                      ? [t("web.admin.settings.signIn.passwordsOff.linkAutoCreate", { provider })]
                      : []),
                  ]}
                  accounts={unmatched.data?.items ?? []}
                  total={totals.admin_link}
                  showReason
                />
              ) : null}
              <Link
                to="/admin/users"
                className="text-muted-foreground hover:text-foreground inline-flex items-center gap-1.5 text-xs font-medium transition-colors"
              >
                {t("web.admin.settings.signIn.passwordsOff.usersLink")}
                <ArrowRight className="h-3 w-3" aria-hidden="true" />
              </Link>
            </>
          )}
          <p className="text-muted-foreground">
            {t("web.admin.settings.signIn.passwordsOff.sessions")}
          </p>
        </div>
        <AlertDialogFooter>
          <AlertDialogCancel>{t("common.cancel")}</AlertDialogCancel>
          <AlertDialogAction variant="destructive" onClick={onConfirm} disabled={loading}>
            {t("web.admin.settings.signIn.passwordsOff.confirm")}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}

interface AccountGroupProps {
  heading: string;
  notes: string[];
  accounts: Account[];
  total: number;
  /** Says why each account isn't connected by email. */
  showReason?: boolean;
}

function AccountGroup({ heading, notes, accounts, total, showReason = false }: AccountGroupProps) {
  const t = useT();
  const id = useId();
  const more = total - accounts.length;
  return (
    <section aria-labelledby={id} className="grid gap-1.5">
      <h3 id={id} className="font-medium">
        {heading}
      </h3>
      {notes.map((note) => (
        <p key={note} className="text-muted-foreground">
          {note}
        </p>
      ))}
      <ul className="m-0 grid gap-1 ps-0">
        {accounts.map((account) => {
          const reason = showReason ? REASON_TEXT[account.reason] : undefined;
          return (
            <li key={account.id} className="flex min-w-0 flex-wrap items-baseline gap-x-2">
              <span className="font-medium break-all">{account.username}</span>
              {account.email ? (
                <span className="text-muted-foreground min-w-0 break-all">{account.email}</span>
              ) : null}
              {reason ? <span className="text-muted-foreground text-xs">{t(reason)}</span> : null}
            </li>
          );
        })}
      </ul>
      {more > 0 ? (
        <p className="text-muted-foreground">
          {t("web.admin.settings.signIn.passwordsOff.more", { count: more })}
        </p>
      ) : null}
    </section>
  );
}
