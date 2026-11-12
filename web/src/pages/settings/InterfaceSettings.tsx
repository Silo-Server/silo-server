import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { ArrowDown, ArrowUp, X } from "lucide-react";
import { toast } from "sonner";

import { PageHeader } from "@/components/page/PageHeader";
import { SettingsGroup } from "@/components/settings/SettingsGroup";
import { Button } from "@/components/ui/button";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { useUICustomization } from "@/hooks/useUICustomization";
import { useUserLibraries } from "@/hooks/queries/libraries";
import { useClearSettingValue, useSetSettingValue } from "@/hooks/queries/settingValues";
import { useT, type MessageKey, type Translate } from "@/i18n";
import { SETTING_KEYS } from "@/lib/settingsContract";
import {
  defaultWebPrimaryMenu,
  menuItemKey,
  moveMenuItem,
  cardPresentationValue,
  type CardPresentation,
  type PosterSize,
  type PrimaryMenuItem,
} from "@/lib/uiCustomization";

import { CardOverlaySettings } from "./CardOverlaySettings";
import { ChoiceButtons } from "@/components/settings/ChoiceButtons";
import { PosterCardPreview } from "./PosterCardPreview";
import { ResetButton } from "./ResetButton";

const CLIENT_SCOPE = { scope: "profile_client" } as const;
const DEVICE_SCOPE = { scope: "profile_device" } as const;

/** How long the preview keeps the hover-only shortcut buttons showing after a pick. */
const SHORTCUT_REVEAL_MS = 2500;

const BUILTIN_LABELS: Record<string, MessageKey> = {
  home: "web.settings.interface.builtin.home",
  movies: "web.settings.interface.builtin.movies",
  series: "web.settings.interface.builtin.series",
  music: "web.settings.interface.builtin.music",
  audiobooks: "web.settings.interface.builtin.audiobooks",
  for_you: "web.settings.interface.builtin.forYou",
  calendar: "web.settings.interface.builtin.calendar",
};

const ADDABLE_WEB_BUILTINS: PrimaryMenuItem[] = [
  // Media-family built-ins are global by contract. Until web has global
  // routes for every family, users can add the explicit library destinations
  // below without giving a global item first-library semantics.
  { type: "builtin", destination: "for_you" },
  { type: "builtin", destination: "calendar" },
];

const POSTER_SIZES: readonly { value: PosterSize; labelKey: MessageKey }[] = [
  { value: "compact", labelKey: "web.settings.interface.posterCompact" },
  { value: "standard", labelKey: "web.settings.interface.posterStandard" },
  { value: "large", labelKey: "web.settings.interface.posterLarge" },
];

function menuItemLabel(t: Translate, item: PrimaryMenuItem): string {
  if (item.type === "builtin") {
    const key = BUILTIN_LABELS[item.destination];
    return key ? t(key) : item.destination;
  }
  if (item.type === "library") return t("web.settings.interface.menuLibrary", { name: item.label });
  if (item.type === "section") return t("web.settings.interface.menuSection", { name: item.label });
  return t("web.settings.interface.menuCollection", { name: item.label });
}

function errorMessage(error: unknown, fallback: string) {
  return error instanceof Error ? error.message : fallback;
}

export default function InterfaceSettings() {
  const t = useT();
  const [revealShortcuts, setRevealShortcuts] = useState(false);
  const revealTimer = useRef<number | undefined>(undefined);
  useEffect(() => () => window.clearTimeout(revealTimer.current), []);
  const flashShortcuts = useCallback(() => {
    setRevealShortcuts(true);
    window.clearTimeout(revealTimer.current);
    revealTimer.current = window.setTimeout(() => setRevealShortcuts(false), SHORTCUT_REVEAL_MS);
  }, []);

  return (
    <div className="space-y-6">
      <PageHeader
        title={t("web.settings.interface.title")}
        subtitle={t("web.settings.interface.subtitle")}
      />
      {/* The preview sits beside the poster controls once there is room, and
          stays in view while they scroll; at large poster sizes it can be
          taller than the window, so it scrolls on its own. On narrow screens
          it leads. */}
      <div className="grid gap-x-10 @5xl:grid-cols-[minmax(0,1fr)_auto]">
        <aside className="pb-2 @5xl:order-last @5xl:pb-0">
          <div className="@5xl:sticky @5xl:top-6 @5xl:max-h-[calc(100dvh-1.5rem)] @5xl:overflow-y-auto @5xl:pt-6">
            <PosterCardPreview revealShortcuts={revealShortcuts} />
          </div>
        </aside>
        <div className="min-w-0">
          <PosterSettings />
          <CardOverlaySettings onShortcutsPicked={flashShortcuts} />
          <MenuSettings />
        </div>
      </div>
    </div>
  );
}

/** A short, plain note with one action, for a setting this browser overrides. */
function OverrideNotice({
  children,
  action,
  disabled,
  onAction,
}: {
  children: ReactNode;
  action: string;
  disabled: boolean;
  onAction: () => void;
}) {
  return (
    <div className="border-border/70 bg-muted/25 flex flex-col gap-3 rounded-xl border p-4 sm:flex-row sm:items-center sm:justify-between">
      <p className="text-muted-foreground text-sm">{children}</p>
      <Button type="button" variant="outline" size="sm" disabled={disabled} onClick={onAction}>
        {action}
      </Button>
    </div>
  );
}

/**
 * What to show instead of the web layout controls while they cannot be
 * edited, or null when they can.
 */
function useCustomizationBlocker(): ReactNode {
  const t = useT();
  const customization = useUICustomization();
  if (customization.isLoading) {
    return <p className="text-muted-foreground text-sm">{t("web.settings.interface.loading")}</p>;
  }
  if (customization.isUnavailable) {
    return (
      <div className="surface-panel-subtle rounded-xl border p-4" role="alert">
        <p className="font-medium">{t("web.settings.interface.loadFailedTitle")}</p>
        <p className="text-muted-foreground mt-1 text-sm">
          {t("web.settings.interface.loadFailedDescription")}
        </p>
      </div>
    );
  }
  if (!customization.isSupported) {
    return (
      <div className="surface-panel-subtle rounded-xl border p-4" role="alert">
        <p className="font-medium">{t("web.settings.interface.unsupportedTitle")}</p>
        <p className="text-muted-foreground mt-1 text-sm">
          {t("web.settings.interface.unsupportedDescription")}
        </p>
      </div>
    );
  }
  return null;
}

/** Poster size, stored for the web client family. */
function PosterSettings() {
  const t = useT();
  const customization = useUICustomization();
  const setValue = useSetSettingValue();
  const clearValue = useClearSettingValue();
  const blocker = useCustomizationBlocker();
  const presentation = customization.cardPresentation;
  const deviceOverride = customization.cardPresentationSource === "profile_device";
  const clientOverride = customization.cardPresentationSource === "profile_client";
  const busy = setValue.isPending || clearValue.isPending;

  async function save(next: CardPresentation) {
    try {
      await setValue.mutateAsync({
        key: SETTING_KEYS.UI_CARD_PRESENTATION,
        value: cardPresentationValue(next),
        identity: CLIENT_SCOPE,
      });
    } catch (error) {
      toast.error(errorMessage(error, t("web.settings.interface.posterSaveFailed")));
    }
  }

  async function clear(identity: typeof CLIENT_SCOPE | typeof DEVICE_SCOPE) {
    try {
      await clearValue.mutateAsync({ key: SETTING_KEYS.UI_CARD_PRESENTATION, identity });
    } catch (error) {
      toast.error(errorMessage(error, t("web.settings.interface.posterResetFailed")));
    }
  }

  return (
    <SettingsGroup
      title={t("web.settings.interface.postersTitle")}
      description={t("web.settings.interface.postersDescription")}
      actions={
        !blocker && clientOverride ? (
          <ResetButton
            label={t("web.settings.interface.posterReset")}
            disabled={busy}
            onClick={() => void clear(CLIENT_SCOPE)}
          />
        ) : null
      }
    >
      {blocker ?? (
        <>
          {deviceOverride ? (
            <OverrideNotice
              action={t("web.settings.interface.matchOtherBrowsers")}
              disabled={busy}
              onAction={() => void clear(DEVICE_SCOPE)}
            >
              {t("web.settings.interface.posterDeviceOverride")}
            </OverrideNotice>
          ) : null}
          <ChoiceButtons
            label={t("web.settings.interface.posterSize")}
            value={presentation.poster_size}
            options={POSTER_SIZES.map((size) => ({ value: size.value, label: t(size.labelKey) }))}
            disabled={busy || deviceOverride}
            onChange={(poster_size) => void save({ ...presentation, poster_size })}
          />
        </>
      )}
    </SettingsGroup>
  );
}

/** The ordered links in the web menu, stored for the web client family. Saves on every edit. */
function MenuSettings() {
  const t = useT();
  const { data: libraries = [] } = useUserLibraries();
  const customization = useUICustomization();
  const setValue = useSetSettingValue();
  const clearValue = useClearSettingValue();
  const blocker = useCustomizationBlocker();
  const baselineMenu = useMemo(
    () => customization.primaryMenu ?? defaultWebPrimaryMenu(),
    [customization.primaryMenu],
  );
  const baselineKey = useMemo(() => JSON.stringify(baselineMenu.items), [baselineMenu.items]);
  // The menu as last saved from here, shown until the effective menu it was
  // saved over is replaced by the refetch (or while the save is in flight), so
  // an edit never blinks back to the old order.
  const [saved, setSaved] = useState<{ items: PrimaryMenuItem[]; overBaselineKey: string } | null>(
    null,
  );
  const busy = setValue.isPending || clearValue.isPending;
  const showSaved = saved !== null && (setValue.isPending || saved.overBaselineKey === baselineKey);
  // Once the refetch has replaced the menu it was saved over, the effective
  // menu is the truth again; drop the copy so it can never resurface.
  if (saved !== null && !showSaved) setSaved(null);
  const menuItems = showSaved ? saved.items : baselineMenu.items;
  const deviceOverride = customization.primaryMenuSource === "profile_device";
  const clientOverride = customization.primaryMenuSource === "profile_client" || saved !== null;
  const locked = busy || deviceOverride;
  const atLimit = menuItems.length >= 64;

  const availableItems = useMemo(() => {
    const current = new Set(menuItems.map(menuItemKey));
    const visibleLibraryIds = new Set(libraries.map((library) => library.id));
    const candidates: PrimaryMenuItem[] = [
      ...ADDABLE_WEB_BUILTINS,
      ...libraries.map(
        (library): PrimaryMenuItem => ({
          type: "library",
          library_id: library.id,
          label: library.name,
        }),
      ),
      ...customization.shortcuts.items.filter(
        (item) => item.library_id === undefined || visibleLibraryIds.has(item.library_id),
      ),
    ];
    const unique = new Map<string, PrimaryMenuItem>();
    for (const candidate of candidates) {
      const key = menuItemKey(candidate);
      if (!current.has(key)) unique.set(key, candidate);
    }
    return [...unique.values()];
  }, [customization.shortcuts.items, libraries, menuItems]);

  async function saveMenu(next: PrimaryMenuItem[]) {
    setSaved({ items: next, overBaselineKey: baselineKey });
    try {
      await setValue.mutateAsync({
        key: SETTING_KEYS.NAV_PRIMARY_MENU,
        value: { items: next },
        identity: CLIENT_SCOPE,
      });
    } catch (error) {
      setSaved(null);
      toast.error(errorMessage(error, t("web.settings.interface.menuSaveFailed")));
    }
  }

  async function clear(identity: typeof CLIENT_SCOPE | typeof DEVICE_SCOPE) {
    try {
      await clearValue.mutateAsync({ key: SETTING_KEYS.NAV_PRIMARY_MENU, identity });
      setSaved(null);
    } catch (error) {
      toast.error(errorMessage(error, t("web.settings.interface.menuResetFailed")));
    }
  }

  // The poster section already explains why neither can be edited.
  if (blocker) return null;

  return (
    <SettingsGroup
      title={t("web.settings.interface.menuTitle")}
      description={t("web.settings.interface.menuDescription")}
      actions={
        clientOverride ? (
          <ResetButton
            label={t("web.settings.interface.menuReset")}
            disabled={locked}
            onClick={() => void clear(CLIENT_SCOPE)}
          />
        ) : null
      }
    >
      <>
        {deviceOverride ? (
          <OverrideNotice
            action={t("web.settings.interface.matchOtherBrowsers")}
            disabled={busy}
            onAction={() => void clear(DEVICE_SCOPE)}
          >
            {t("web.settings.interface.menuDeviceOverride")}
          </OverrideNotice>
        ) : null}
        <ol className="space-y-2" aria-label={t("web.settings.interface.menuLinks")}>
          {menuItems.map((item, index) => {
            const label = menuItemLabel(t, item);
            const home = item.type === "builtin" && item.destination === "home";
            return (
              <li
                key={menuItemKey(item)}
                className="surface-panel-subtle border-border/65 flex items-center gap-3 rounded-xl border px-3 py-2"
              >
                <span className="min-w-0 flex-1 truncate text-sm font-medium">{label}</span>
                <div className="flex shrink-0 items-center gap-1">
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon"
                    disabled={locked || index === 0}
                    aria-label={t("web.settings.interface.moveUp", { label })}
                    onClick={() => void saveMenu(moveMenuItem(menuItems, index, -1))}
                  >
                    <ArrowUp className="h-4 w-4" />
                  </Button>
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon"
                    disabled={locked || index === menuItems.length - 1}
                    aria-label={t("web.settings.interface.moveDown", { label })}
                    onClick={() => void saveMenu(moveMenuItem(menuItems, index, 1))}
                  >
                    <ArrowDown className="h-4 w-4" />
                  </Button>
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon"
                    disabled={locked || home}
                    aria-label={
                      home
                        ? t("web.settings.interface.homeNotRemovable")
                        : t("web.settings.interface.remove", { label })
                    }
                    onClick={() =>
                      void saveMenu(menuItems.filter((_, itemIndex) => itemIndex !== index))
                    }
                  >
                    <X className="h-4 w-4" />
                  </Button>
                </div>
              </li>
            );
          })}
        </ol>
        {availableItems.length > 0 ? (
          <Select
            value=""
            disabled={locked || atLimit}
            onValueChange={(key) => {
              const item = availableItems.find((candidate) => menuItemKey(candidate) === key);
              if (item) void saveMenu([...menuItems, item]);
            }}
          >
            <SelectTrigger
              className="w-full sm:max-w-xs"
              aria-label={t("web.settings.interface.addLink")}
            >
              <SelectValue placeholder={t("web.settings.interface.addLinkPlaceholder")} />
            </SelectTrigger>
            <SelectContent>
              {availableItems.map((item) => (
                <SelectItem key={menuItemKey(item)} value={menuItemKey(item)}>
                  {menuItemLabel(t, item)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        ) : null}
      </>
    </SettingsGroup>
  );
}
