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
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import {
  useLocalPasswordOffAccounts,
  type LocalPasswordOffAccounts,
} from "@/hooks/queries/admin/externalSignIn";
import { useT, type MessageKey } from "@/i18n";

/** How many accounts of each group a page of the dialog's lists holds. */
export const LOCAL_PASSWORD_OFF_PAGE = 5;

type Account = LocalPasswordOffAccounts["items"][number];
type GroupQuery = ReturnType<typeof useLocalPasswordOffAccounts>;

const REASON_TEXT: Partial<Record<Account["reason"], MessageKey>> = {
  administrator: "web.admin.settings.signIn.passwordsOff.reason.administrator",
  no_email: "web.admin.settings.signIn.passwordsOff.reason.noEmail",
  email_match_off: "web.admin.settings.signIn.passwordsOff.reason.emailMatchOff",
};

const usersLinkClass =
  "text-muted-foreground hover:text-foreground inline-flex items-center gap-1.5 text-xs font-medium transition-colors";

/** What the confirmation is answered for: the page's values, saved or staged. */
export interface LocalPasswordOffContext {
  /** The provider people sign in with once passwords are off. */
  provider: string;
  /** Whether that provider creates an account at a first sign-in. */
  autoProvision: boolean;
  /** Whether email matching is on. */
  emailMatch: boolean;
}

interface LocalPasswordOffDialogProps extends LocalPasswordOffContext {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onConfirm: () => void;
  /** Where focus goes when the dialog closes: it has no trigger to return to. */
  onCloseAutoFocus: (event: Event) => void;
}

/**
 * A group's state for the dialog, which never presents an answer older than
 * its current opening. A first load or a reload (the dialog reopened, or its
 * values changed) is loading, and a reload that fails is a load failure
 * whatever an earlier opening cached. A failed Show more is neither: the
 * pages already shown stay, with a retry (AccountGroup).
 */
function groupState(query: GroupQuery) {
  const pages = query.data?.pages ?? [];
  return {
    loading: query.isPending || (query.isFetching && !query.isFetchingNextPage),
    failed: query.isError && !query.isFetchNextPageError,
    accounts: pages.flatMap((page) => page.items),
    /** The newest page's counts: accounts can change between pages. */
    totals: pages.at(-1)?.totals,
  };
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
  onCloseAutoFocus,
  provider,
  autoProvision,
  emailMatch,
}: LocalPasswordOffDialogProps) {
  const t = useT();
  const matchedQuery = useLocalPasswordOffAccounts(
    "email_match",
    emailMatch,
    LOCAL_PASSWORD_OFF_PAGE,
    open,
  );
  const unmatchedQuery = useLocalPasswordOffAccounts(
    "admin_link",
    emailMatch,
    LOCAL_PASSWORD_OFF_PAGE,
    open,
  );
  const matched = groupState(matchedQuery);
  const unmatched = groupState(unmatchedQuery);
  const loading = matched.loading || unmatched.loading;
  const failed = matched.failed || unmatched.failed;
  // Each group's count comes from its own response: the two are answered at
  // different moments, so one's totals can be older than the other's list.
  const matchedTotal = matched.totals?.email_match;
  const unmatchedTotal = unmatched.totals?.admin_link;
  const nobody = matchedTotal === 0 && unmatchedTotal === 0;

  return (
    <AlertDialog open={open} onOpenChange={onOpenChange}>
      <AlertDialogContent onCloseAutoFocus={onCloseAutoFocus}>
        <AlertDialogHeader>
          <AlertDialogTitle>{t("web.admin.settings.signIn.passwordsOff.title")}</AlertDialogTitle>
          <AlertDialogDescription>
            {t("web.admin.settings.signIn.passwordsOff.description", { provider })}
          </AlertDialogDescription>
        </AlertDialogHeader>
        <div
          className="-mx-1 grid max-h-[55vh] gap-4 overflow-y-auto px-1 text-sm"
          aria-live="polite"
          aria-busy={loading}
        >
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
              {matchedTotal ? (
                <AccountGroup
                  heading={t("web.admin.settings.signIn.passwordsOff.matchHeading", {
                    count: matchedTotal,
                  })}
                  notes={[t("web.admin.settings.signIn.passwordsOff.matchExplain", { provider })]}
                  accounts={matched.accounts}
                  latestTotal={matchedTotal}
                  query={matchedQuery}
                />
              ) : null}
              {unmatchedTotal ? (
                <AccountGroup
                  heading={t("web.admin.settings.signIn.passwordsOff.linkHeading", {
                    count: unmatchedTotal,
                  })}
                  notes={[
                    t("web.admin.settings.signIn.passwordsOff.linkExplain", { provider }),
                    ...(autoProvision
                      ? [t("web.admin.settings.signIn.passwordsOff.linkAutoCreate", { provider })]
                      : []),
                  ]}
                  accounts={unmatched.accounts}
                  latestTotal={unmatchedTotal}
                  query={unmatchedQuery}
                  showReason
                />
              ) : null}
              <Link to="/admin/users" className={usersLinkClass}>
                {t("web.admin.settings.signIn.passwordsOff.usersLink")}
                <ArrowRight className="h-3 w-3" aria-hidden="true" />
              </Link>
            </>
          )}
        </div>
        <p className="text-muted-foreground text-sm">
          {t("web.admin.settings.signIn.passwordsOff.sessions", {
            link: (chunks) => (
              <Link to="/admin/users" className="underline underline-offset-4">
                {chunks}
              </Link>
            ),
          })}
        </p>
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
  /** The group's count from the newest page loaded. */
  latestTotal: number;
  query: GroupQuery;
  /** Says why each account isn't connected by email. */
  showReason?: boolean;
}

function AccountGroup({
  heading,
  notes,
  accounts,
  latestTotal,
  query,
  showReason = false,
}: AccountGroupProps) {
  const t = useT();
  const id = useId();
  // Accounts can change between pages, so the count can be off; whether more
  // remain is the query's to say.
  const more = latestTotal - accounts.length;
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
              {account.username ? (
                <span className="font-medium break-all">{account.username}</span>
              ) : null}
              {account.email ? (
                <span className="text-muted-foreground min-w-0 break-all">{account.email}</span>
              ) : null}
              {reason ? <span className="text-muted-foreground text-xs">{t(reason)}</span> : null}
            </li>
          );
        })}
      </ul>
      {query.isFetchNextPageError ? (
        <p role="alert" className="text-destructive">
          {t("web.admin.settings.signIn.passwordsOff.moreFailed")}
        </p>
      ) : null}
      {query.hasNextPage ? (
        <Button
          type="button"
          variant="ghost"
          size="sm"
          className="justify-self-start"
          disabled={query.isFetchingNextPage}
          onClick={() => void query.fetchNextPage()}
        >
          {more > 0
            ? t("web.admin.settings.signIn.passwordsOff.more", { count: more })
            : t("web.admin.downloads.deviceCopies.showMore")}
        </Button>
      ) : null}
    </section>
  );
}
