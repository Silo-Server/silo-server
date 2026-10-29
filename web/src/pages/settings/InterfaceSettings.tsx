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
import { SETTING_KEYS } from "@/lib/settingsContract";
import {
  defaultWebPrimaryMenu,
  menuItemKey,
  moveMenuItem,
  parsePrimaryMenu,
  cardPresentationValue,
  type CardPresentation,
  type PosterSize,
  type PrimaryMenuItem,
} from "@/lib/uiCustomization";
import { adminDefaultServerKey, storedAdminDefault } from "@/lib/settingsAdminDefaults";
import { useAdminServerSettings, useUpdateServerSetting } from "@/hooks/queries/admin/settings";

import { useAppTheme } from "@/themes/appThemeContext";
import { THEME_IDS, THEME_LABELS } from "@/themes/theme";

import { CardOverlaySettings } from "./CardOverlaySettings";
import { ChoiceButtons } from "./ChoiceButtons";
import { PosterCardPreview } from "./PosterCardPreview";
import { ResetButton } from "./ResetButton";

const CLIENT_SCOPE = { scope: "profile_client" } as const;
const DEVICE_SCOPE = { scope: "profile_device" } as const;

/** The server setting holding the administrator's menu for every profile, as JSON text. */
const ADMIN_MENU_DEFAULT_KEY = adminDefaultServerKey(SETTING_KEYS.NAV_PRIMARY_MENU);

/** How long the preview keeps the hover-only shortcut buttons showing after a pick. */
const SHORTCUT_REVEAL_MS = 2500;

const BUILTIN_LABELS: Record<string, string> = {
  home: "Home",
  movies: "Movies",
  series: "TV Shows",
  music: "Music",
  audiobooks: "Audiobooks",
  for_you: "For You",
  calendar: "Calendar",
};

const ADDABLE_WEB_BUILTINS: PrimaryMenuItem[] = [
  // Media-family built-ins are global by contract. Until web has global
  // routes for every family, users can add the explicit library destinations
  // below without giving a global item first-library semantics.
  { type: "builtin", destination: "for_you" },
  { type: "builtin", destination: "calendar" },
];

const POSTER_SIZES: readonly { value: PosterSize; label: string }[] = [
  { value: "compact", label: "Compact" },
  { value: "standard", label: "Standard" },
  { value: "large", label: "Large" },
];

/** Ordered as THEME_IDS lists them, so the default is offered first. */
const THEME_OPTIONS = THEME_IDS.map((id) => ({ value: id, label: THEME_LABELS[id] }));

function menuItemLabel(item: PrimaryMenuItem): string {
  if (item.type === "builtin") return BUILTIN_LABELS[item.destination] ?? item.destination;
  if (item.type === "library") return `${item.label} · Library`;
  if (item.type === "section") return `${item.label} · Section`;
  return `${item.label} · Collection`;
}

function errorMessage(error: unknown, fallback: string) {
  return error instanceof Error ? error.message : fallback;
}

export default function InterfaceSettings() {
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
        title="Navigation & Cards"
        subtitle="Choose how posters look and which links sit in your menu. Changes save as you make them."
      />
      {/* The preview sits beside the poster controls once there is room, and
          stays in view while they scroll. On narrow screens it leads. */}
      <div className="grid gap-x-10 @5xl:grid-cols-[minmax(0,1fr)_19rem]">
        <aside className="pb-2 @5xl:order-last @5xl:pb-0">
          <div className="@5xl:sticky @5xl:top-6 @5xl:pt-6">
            <PosterCardPreview revealShortcuts={revealShortcuts} />
          </div>
        </aside>
        <div className="min-w-0">
          <ThemeSettings />
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
  const customization = useUICustomization();
  if (customization.isLoading) {
    return <p className="text-muted-foreground text-sm">Loading…</p>;
  }
  if (customization.isUnavailable) {
    return (
      <div className="surface-panel-subtle rounded-xl border p-4" role="alert">
        <p className="font-medium">Couldn't load your poster and menu settings</p>
        <p className="text-muted-foreground mt-1 text-sm">
          Editing is off until they load, so nothing you chose earlier gets overwritten.
        </p>
      </div>
    );
  }
  if (!customization.isSupported) {
    return (
      <div className="surface-panel-subtle rounded-xl border p-4" role="alert">
        <p className="font-medium">Your server needs an update</p>
        <p className="text-muted-foreground mt-1 text-sm">
          Poster size and the menu can be changed once your server runs a newer version of Silo.
        </p>
      </div>
    );
  }
  return null;
}

/**
 * The colour theme. Unlike everything else on this page it is not a server
 * setting: it is stored on the device, so switching it is instant and works
 * before sign-in. WeFlix uses the top bar; Midnight Cinema keeps the left rail.
 */
function ThemeSettings() {
  const { theme, setTheme } = useAppTheme();

  return (
    <SettingsGroup title="Theme" description="How the whole app looks. Saved on this device.">
      <ChoiceButtons
        label="Theme"
        hideLabel
        value={theme}
        options={THEME_OPTIONS}
        onChange={setTheme}
      />
    </SettingsGroup>
  );
}

/** Poster size, stored for the web client family. */
function PosterSettings() {
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
      toast.error(errorMessage(error, "Couldn't save poster settings"));
    }
  }

  async function clear(identity: typeof CLIENT_SCOPE | typeof DEVICE_SCOPE) {
    try {
      await clearValue.mutateAsync({ key: SETTING_KEYS.UI_CARD_PRESENTATION, identity });
    } catch (error) {
      toast.error(errorMessage(error, "Couldn't reset poster settings"));
    }
  }

  return (
    <SettingsGroup
      title="Posters"
      description="How big posters are in your web browsers."
      actions={
        !blocker && clientOverride ? (
          <ResetButton
            label="Reset poster settings"
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
              action="Match my other browsers"
              disabled={busy}
              onAction={() => void clear(DEVICE_SCOPE)}
            >
              This browser has its own poster settings, so changes here won't show in it.
            </OverrideNotice>
          ) : null}
          <ChoiceButtons
            label="Size"
            value={presentation.poster_size}
            options={POSTER_SIZES}
            disabled={busy || deviceOverride}
            onChange={(poster_size) => void save({ ...presentation, poster_size })}
          />
        </>
      )}
    </SettingsGroup>
  );
}

/**
 * The menu every profile gets.
 *
 * This edits the administrator default (`defaults.nav.primary_menu`), not the
 * signed-in profile's own menu. A profile's stored menu wins over the default,
 * so writing the default is what makes one menu reach everybody — including a
 * new account, whose profile starts with no menu of its own. The page is
 * admin-only for the same reason.
 *
 * Saves on every edit.
 */
function MenuSettings() {
  const { data: libraries = [] } = useUserLibraries();
  const customization = useUICustomization();
  const adminSettings = useAdminServerSettings();
  const updateDefault = useUpdateServerSetting(adminSettings.data);
  const clearValue = useClearSettingValue();
  const blocker = useCustomizationBlocker();
  const sharedMenu = useMemo(
    () =>
      parsePrimaryMenu(
        storedAdminDefault(
          SETTING_KEYS.NAV_PRIMARY_MENU,
          adminSettings.data?.[ADMIN_MENU_DEFAULT_KEY],
        ),
      ),
    [adminSettings.data],
  );
  const baselineMenu = sharedMenu ?? defaultWebPrimaryMenu();
  const baselineKey = useMemo(() => JSON.stringify(baselineMenu.items), [baselineMenu.items]);
  // The menu as last saved from here, shown until the effective menu it was
  // saved over is replaced by the refetch (or while the save is in flight), so
  // an edit never blinks back to the old order.
  const [saved, setSaved] = useState<{ items: PrimaryMenuItem[]; overBaselineKey: string } | null>(
    null,
  );
  const busy = updateDefault.isPending || clearValue.isPending;
  const showSaved =
    saved !== null && (updateDefault.isPending || saved.overBaselineKey === baselineKey);
  // Once the refetch has replaced the menu it was saved over, the effective
  // menu is the truth again; drop the copy so it can never resurface.
  if (saved !== null && !showSaved) setSaved(null);
  const menuItems = showSaved ? saved.items : baselineMenu.items;
  const deviceOverride = customization.primaryMenuSource === "profile_device";
  const sharedMenuSet = sharedMenu !== null || saved !== null;
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

  // Writes the administrator default, so every profile without a menu of its
  // own — including a new account's — follows what is set here.
  async function saveMenu(next: PrimaryMenuItem[]) {
    setSaved({ items: next, overBaselineKey: baselineKey });
    try {
      await updateDefault.mutateAsync({
        key: ADMIN_MENU_DEFAULT_KEY,
        value: JSON.stringify({ items: next }),
      });
      toast.success("Saved the menu for every profile");
    } catch (error) {
      setSaved(null);
      toast.error(errorMessage(error, "Couldn't save the menu"));
    }
  }

  /** Drops the default, so every profile goes back to the built-in links. */
  async function resetSharedMenu() {
    setSaved(null);
    try {
      await updateDefault.mutateAsync({ key: ADMIN_MENU_DEFAULT_KEY, value: "null" });
      toast.success("Reset the menu for every profile");
    } catch (error) {
      toast.error(errorMessage(error, "Couldn't reset the menu"));
    }
  }

  /** Clears this browser's own menu row, which is what shadows the default here. */
  async function clearDeviceOverride() {
    try {
      await clearValue.mutateAsync({ key: SETTING_KEYS.NAV_PRIMARY_MENU, identity: DEVICE_SCOPE });
    } catch (error) {
      toast.error(errorMessage(error, "Couldn't clear this browser's menu"));
    }
  }

  // The poster section already explains why neither can be edited.
  if (blocker) return null;

  return (
    <SettingsGroup
      title="Menu"
      description="The links along the top of Silo, for every profile. Search and your profile are always there."
      actions={
        sharedMenuSet ? (
          <ResetButton
            label="Reset menu for everyone"
            disabled={locked}
            onClick={resetSharedMenu}
          />
        ) : null
      }
    >
      <>
        {deviceOverride ? (
          <OverrideNotice
            action="Match my other browsers"
            disabled={busy}
            onAction={() => void clearDeviceOverride()}
          >
            This browser has its own menu, so changes here won't show in it.
          </OverrideNotice>
        ) : null}
        <ol className="space-y-2" aria-label="Menu links">
          {menuItems.map((item, index) => {
            const label = menuItemLabel(item);
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
                    aria-label={`Move ${label} up`}
                    onClick={() => void saveMenu(moveMenuItem(menuItems, index, -1))}
                  >
                    <ArrowUp className="h-4 w-4" />
                  </Button>
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon"
                    disabled={locked || index === menuItems.length - 1}
                    aria-label={`Move ${label} down`}
                    onClick={() => void saveMenu(moveMenuItem(menuItems, index, 1))}
                  >
                    <ArrowDown className="h-4 w-4" />
                  </Button>
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon"
                    disabled={locked || home}
                    aria-label={home ? "Home can't be removed" : `Remove ${label}`}
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
            <SelectTrigger className="w-full sm:max-w-xs" aria-label="Add a link">
              <SelectValue placeholder="Add a link…" />
            </SelectTrigger>
            <SelectContent>
              {availableItems.map((item) => (
                <SelectItem key={menuItemKey(item)} value={menuItemKey(item)}>
                  {menuItemLabel(item)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        ) : null}
      </>
    </SettingsGroup>
  );
}
