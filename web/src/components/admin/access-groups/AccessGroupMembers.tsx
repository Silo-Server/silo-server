import { Search } from "lucide-react";
import { useId, useState } from "react";
import { Link } from "react-router";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { useAdminUsers } from "@/hooks/queries/admin/users";
import { useT } from "@/i18n";

/** Members shown at first, and how many more each "Show more" reveals. */
const MEMBER_PAGE_SIZE = 10;

/** Read-only list of a group's members, each linking to their user page. */
export function AccessGroupMembers({ groupId }: { groupId: number | string }) {
  const t = useT();
  const headingId = useId();
  const users = useAdminUsers();
  const [query, setQuery] = useState("");
  const [visibleCount, setVisibleCount] = useState(MEMBER_PAGE_SIZE);
  const members = (users.data ?? [])
    .filter((user) => user.role !== "admin" && String(user.access_group_id) === String(groupId))
    .sort((a, b) => a.username.localeCompare(b.username));
  const searchable = members.length > MEMBER_PAGE_SIZE;
  const needle = searchable ? query.trim().toLocaleLowerCase() : "";
  const matches = needle
    ? members.filter(
        (member) =>
          member.username.toLocaleLowerCase().includes(needle) ||
          member.email.toLocaleLowerCase().includes(needle),
      )
    : members;
  const shown = matches.slice(0, visibleCount);

  return (
    <section
      className="surface-panel space-y-3 rounded-2xl border-0 p-5"
      aria-labelledby={headingId}
    >
      <div>
        <div className="flex items-center gap-2">
          <h2 id={headingId} className="text-sm font-semibold">
            {t("web.admin.access.groups.members.heading")}
          </h2>
          {users.isSuccess && members.length > 0 && (
            <span className="bg-secondary text-secondary-foreground rounded-full px-2 py-0.5 text-xs font-medium tabular-nums">
              {members.length}
            </span>
          )}
        </div>
        <p className="text-muted-foreground mt-0.5 text-xs">
          {t("web.admin.access.groups.members.help")}
        </p>
      </div>
      {users.isPending && (
        <p className="text-muted-foreground text-sm">
          {t("web.admin.access.groups.members.loading")}
        </p>
      )}
      {users.isError && (
        <p role="alert" className="text-sm">
          {t("web.admin.access.groups.members.loadFailed", {
            retry: (chunks) => (
              <Button variant="link" className="h-auto p-0" onClick={() => void users.refetch()}>
                {chunks}
              </Button>
            ),
          })}
        </p>
      )}
      {users.isSuccess && members.length === 0 && (
        <p className="text-muted-foreground text-sm">
          {t("web.admin.access.groups.members.empty")}
        </p>
      )}
      {searchable && (
        <div className="relative sm:max-w-sm">
          <Search
            className="text-muted-foreground pointer-events-none absolute start-3 top-1/2 size-4 -translate-y-1/2"
            aria-hidden="true"
          />
          <Input
            type="search"
            value={query}
            onChange={(event) => {
              setQuery(event.target.value);
              setVisibleCount(MEMBER_PAGE_SIZE);
            }}
            placeholder={t("web.admin.users.list.searchPlaceholder")}
            aria-label={t("web.admin.access.groups.members.search")}
            className="ps-9"
          />
        </div>
      )}
      {members.length > 0 && matches.length === 0 && (
        <p role="status" className="text-muted-foreground text-sm">
          {t("web.admin.access.groups.members.noMatches")}
        </p>
      )}
      {shown.length > 0 && (
        <ul className="divide-border -mx-2 divide-y text-sm">
          {shown.map((member) => (
            <li key={member.id}>
              <Link
                to={`/admin/users/${member.id}`}
                className="hover:bg-accent/40 focus-visible:ring-ring/50 flex min-h-11 min-w-0 flex-col justify-center rounded-md px-2 py-1.5 outline-none focus-visible:ring-[3px]"
              >
                <span className="truncate font-medium">{member.username}</span>
                {member.email ? (
                  <span className="text-muted-foreground truncate text-xs">{member.email}</span>
                ) : null}
              </Link>
            </li>
          ))}
        </ul>
      )}
      {matches.length > shown.length && (
        <Button
          type="button"
          variant="outline"
          className="w-full sm:w-auto"
          onClick={() => setVisibleCount((count) => count + MEMBER_PAGE_SIZE)}
        >
          {t("web.admin.downloads.deviceCopies.showMore")}
        </Button>
      )}
    </section>
  );
}
