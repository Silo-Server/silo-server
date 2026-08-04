import { useState } from "react";
import { ChevronRight } from "lucide-react";
import type { LucideIcon } from "lucide-react";
import { Link } from "react-router";

import { cn } from "@/lib/utils";

export interface SettingsOverviewNavItem {
  id: string;
  label: string;
  description: string;
  icon: LucideIcon;
  href: string;
}

export interface SettingsOverviewNavGroup {
  label: string;
  items: readonly SettingsOverviewNavItem[];
}

interface SettingsOverviewNavProps {
  groups: readonly SettingsOverviewNavGroup[];
  ariaLabel: string;
  idPrefix: string;
  variant?: "grouped-list" | "directory";
}

function groupSectionId(idPrefix: string, label: string) {
  return `${idPrefix}-${label.toLowerCase().replace(/[^a-z0-9]+/g, "-")}`;
}

export function SettingsOverviewNav({
  groups,
  ariaLabel,
  idPrefix,
  variant = "grouped-list",
}: SettingsOverviewNavProps) {
  const firstGroupId = groups[0] ? groupSectionId(idPrefix, groups[0].label) : "";
  const [activeGroupId, setActiveGroupId] = useState(firstGroupId);
  const isDirectory = variant === "directory";
  const resolvedActiveGroupId = groups.some(
    (group) => groupSectionId(idPrefix, group.label) === activeGroupId,
  )
    ? activeGroupId
    : firstGroupId;

  if (groups.length === 0) {
    return (
      <div className="surface-panel-subtle rounded-2xl px-5 py-8 text-center">
        <p className="font-medium">No matching settings</p>
        <p className="text-muted-foreground mt-1 text-sm">
          Try a category, feature, or setting name.
        </p>
      </div>
    );
  }

  return (
    <div className={cn(isDirectory && "lg:space-y-8")}>
      {isDirectory ? (
        <nav
          aria-label={`${ariaLabel} categories`}
          className="surface-panel-subtle hidden max-w-2xl overflow-hidden rounded-xl border lg:flex"
        >
          {groups.map((group) => {
            const sectionId = groupSectionId(idPrefix, group.label);
            const isActive = sectionId === resolvedActiveGroupId;

            return (
              <a
                key={group.label}
                href={`#${sectionId}`}
                aria-label={`${group.label}, ${group.items.length} settings sections`}
                aria-current={isActive ? "location" : undefined}
                onClick={() => setActiveGroupId(sectionId)}
                className={cn(
                  "text-muted-foreground hover:bg-surface-hover/60 hover:text-foreground focus-visible:ring-ring/60 relative flex min-h-11 flex-1 items-center justify-center gap-2 px-4 text-sm font-medium transition-colors focus-visible:z-10 focus-visible:ring-[3px] focus-visible:outline-none focus-visible:ring-inset",
                  isActive &&
                    "text-foreground after:bg-primary after:absolute after:inset-x-4 after:bottom-0 after:h-0.5 after:rounded-full",
                )}
              >
                <span>{group.label}</span>
                <span className="text-muted-foreground text-xs tabular-nums">
                  {group.items.length}
                </span>
              </a>
            );
          })}
        </nav>
      ) : null}

      <nav
        aria-label={ariaLabel}
        className={cn(
          "grid items-start gap-6 lg:grid-cols-2",
          isDirectory && "block space-y-6 lg:space-y-10",
        )}
      >
        {groups.map((group) => {
          const sectionId = groupSectionId(idPrefix, group.label);

          return (
            <section
              key={group.label}
              aria-labelledby={sectionId}
              className={cn(isDirectory && "scroll-mt-6")}
            >
              <h2
                id={sectionId}
                className={cn(
                  "text-muted-foreground mb-2 px-1 text-[11px] font-semibold tracking-[0.16em] uppercase",
                  isDirectory &&
                    "lg:text-foreground lg:mb-3 lg:px-0 lg:text-base lg:font-semibold lg:tracking-tight lg:normal-case",
                )}
              >
                {group.label}
                {isDirectory ? (
                  <span className="text-muted-foreground ml-1.5 hidden font-medium lg:inline">
                    ({group.items.length})
                  </span>
                ) : null}
              </h2>
              <ul
                className={cn(
                  "surface-panel overflow-hidden rounded-2xl border-0 shadow-none",
                  isDirectory &&
                    "lg:grid lg:grid-cols-2 lg:gap-3 lg:overflow-visible lg:rounded-none lg:bg-transparent 2xl:grid-cols-4",
                )}
              >
                {group.items.map((item) => {
                  const Icon = item.icon;

                  return (
                    <li
                      key={item.id}
                      className={cn(
                        "border-border/60 border-b last:border-b-0",
                        isDirectory && "lg:border-0",
                      )}
                    >
                      <Link
                        to={item.href}
                        className={cn(
                          "hover:bg-surface-hover/70 focus-visible:bg-surface-hover/70 focus-visible:ring-ring/70 flex min-h-[4.5rem] items-center gap-3 px-4 py-3 transition-colors focus-visible:ring-2 focus-visible:outline-none focus-visible:ring-inset",
                          isDirectory &&
                            "lg:bg-card/45 lg:border-border/70 lg:hover:border-ring/40 lg:h-28 lg:rounded-xl lg:border lg:px-4 lg:py-4 lg:shadow-sm lg:hover:-translate-y-0.5 lg:hover:shadow-md lg:focus-visible:-translate-y-0.5",
                        )}
                      >
                        <span
                          className={cn(
                            "bg-accent text-foreground flex h-9 w-9 shrink-0 items-center justify-center rounded-xl",
                            isDirectory && "lg:h-10 lg:w-10",
                          )}
                        >
                          <Icon className="h-[18px] w-[18px]" aria-hidden="true" />
                        </span>
                        <span className="min-w-0 flex-1">
                          <span
                            className={cn(
                              "block text-sm font-semibold",
                              isDirectory && "lg:text-[15px]",
                            )}
                          >
                            {item.label}
                          </span>
                          <span
                            className={cn(
                              "text-muted-foreground mt-0.5 block text-xs leading-snug",
                              isDirectory && "lg:line-clamp-3",
                            )}
                          >
                            {item.description}
                          </span>
                        </span>
                        <ChevronRight
                          className="text-muted-foreground h-4 w-4 shrink-0"
                          aria-hidden="true"
                        />
                      </Link>
                    </li>
                  );
                })}
              </ul>
            </section>
          );
        })}
      </nav>
    </div>
  );
}
