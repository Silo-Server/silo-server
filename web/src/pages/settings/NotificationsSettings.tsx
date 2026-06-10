import { useAuth } from "@/hooks/useAuth";
import { useNotificationPreferences, useSetNotificationPreferences } from "@/hooks/queries/notifications";
import { SettingsGroup } from "@/components/settings/SettingsGroup";
import { SettingRow } from "@/components/settings/SettingRow";
import { Switch } from "@/components/ui/switch";
import type { NotificationPreferenceCategory } from "@/api/types";

const PREF_META: {
  category: NotificationPreferenceCategory;
  label: string;
  description: string;
  adminOnly?: boolean;
}[] = [
  {
    category: "request",
    label: "Requests",
    description: "Updates when your requests are approved, declined, or ready",
  },
  {
    category: "content",
    label: "New content",
    description: "New arrivals matching your watchlist, favorites, and shows in progress",
  },
  {
    category: "system",
    label: "Account & security",
    description: "Password changes and account notices",
  },
  {
    category: "admin",
    label: "Admin alerts",
    description: "Failed jobs and scans",
    adminOnly: true,
  },
  {
    category: "content_digest",
    label: "Daily digest",
    description: "One daily summary of everything added to your libraries",
  },
];

export default function NotificationsSettings() {
  const { user } = useAuth();
  const { data: preferences, isLoading } = useNotificationPreferences();
  const setPreferences = useSetNotificationPreferences();

  const isAdmin = user?.role === "admin";

  const visibleMeta = PREF_META.filter((m) => !m.adminOnly || isAdmin);

  function getEnabled(category: NotificationPreferenceCategory): boolean {
    if (!preferences) return true;
    const entry = preferences.find((p) => p.category === category);
    return entry ? entry.enabled : true;
  }

  function handleToggle(category: NotificationPreferenceCategory, enabled: boolean) {
    setPreferences.mutate({ preferences: [{ category, enabled }] });
  }

  return (
    <div className="space-y-6">
      <div className="space-y-4">
        <div className="space-y-3">
          <h2 className="text-2xl font-semibold tracking-tight sm:text-3xl">Notifications</h2>
          <p className="text-muted-foreground max-w-2xl text-sm leading-relaxed">
            Choose which notifications you receive.
          </p>
        </div>
      </div>

      <SettingsGroup title="Notifications">
        {visibleMeta.map((meta) => (
          <SettingRow
            key={meta.category}
            label={meta.label}
            description={meta.description}
            control={(id) => (
              <Switch
                id={id}
                checked={getEnabled(meta.category)}
                disabled={isLoading || setPreferences.isPending}
                onCheckedChange={(checked) => handleToggle(meta.category, checked)}
              />
            )}
          />
        ))}
      </SettingsGroup>

      <p className="text-muted-foreground px-1 text-[13px] leading-relaxed">
        Announcements from your server admin can&rsquo;t be turned off.
      </p>
    </div>
  );
}
