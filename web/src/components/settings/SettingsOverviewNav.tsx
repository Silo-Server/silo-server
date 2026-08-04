import { ChevronRight } from "lucide-react";
import type { LucideIcon } from "lucide-react";
import { Link } from "react-router";

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
}

export function SettingsOverviewNav({ groups, ariaLabel, idPrefix }: SettingsOverviewNavProps) {
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
    <nav aria-label={ariaLabel} className="grid items-start gap-6 lg:grid-cols-2">
      {groups.map((group) => {
        const sectionId = `${idPrefix}-${group.label.toLowerCase().replace(/[^a-z0-9]+/g, "-")}`;

        return (
          <section key={group.label} aria-labelledby={sectionId}>
            <h2
              id={sectionId}
              className="text-muted-foreground mb-2 px-1 text-[11px] font-semibold tracking-[0.16em] uppercase"
            >
              {group.label}
            </h2>
            <ul className="surface-panel overflow-hidden rounded-2xl border-0 shadow-none">
              {group.items.map((item) => {
                const Icon = item.icon;

                return (
                  <li key={item.id} className="border-border/60 border-b last:border-b-0">
                    <Link
                      to={item.href}
                      className="hover:bg-surface-hover/70 focus-visible:bg-surface-hover/70 focus-visible:ring-ring/70 flex min-h-[4.5rem] items-center gap-3 px-4 py-3 transition-colors focus-visible:ring-2 focus-visible:outline-none focus-visible:ring-inset"
                    >
                      <span className="bg-accent text-foreground flex h-9 w-9 shrink-0 items-center justify-center rounded-xl">
                        <Icon className="h-[18px] w-[18px]" aria-hidden="true" />
                      </span>
                      <span className="min-w-0 flex-1">
                        <span className="block text-sm font-semibold">{item.label}</span>
                        <span className="text-muted-foreground mt-0.5 block text-xs leading-snug">
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
  );
}
