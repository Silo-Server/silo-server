import { useMemo, useState } from "react";
import { ArrowDown, ArrowUp, Check, Monitor, RotateCcw, X } from "lucide-react";
import { toast } from "sonner";

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
  CARD_PRESENTATION_PRESETS,
  defaultWebPrimaryMenu,
  menuItemKey,
  moveMenuItem,
  type CardCaption,
  type CardPresentation,
  type PosterSize,
  type PrimaryMenuItem,
} from "@/lib/uiCustomization";
import { cn } from "@/lib/utils";

const CLIENT_SCOPE = { scope: "profile_client" } as const;

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

function menuItemLabel(item: PrimaryMenuItem): string {
  if (item.type === "builtin") return BUILTIN_LABELS[item.destination] ?? item.destination;
  if (item.type === "library") return `${item.label} · Library`;
  if (item.type === "section") return `${item.label} · Section`;
  return `${item.label} · Collection`;
}

function samePresentation(left: CardPresentation, right: CardPresentation) {
  return left.poster_size === right.poster_size && left.caption === right.caption;
}

function CardPreview({ presentation }: { presentation: CardPresentation }) {
  const widths =
    presentation.poster_size === "compact"
      ? ["w-12", "w-12", "w-12", "w-12"]
      : presentation.poster_size === "large"
        ? ["w-20", "w-20"]
        : ["w-16", "w-16", "w-16"];
  return (
    <div className="bg-background/35 flex min-h-40 items-start gap-3 overflow-hidden rounded-xl border border-white/8 p-4">
      {widths.map((width, index) => (
        <div key={index} className={cn("shrink-0", width)}>
          <div className="from-primary/45 to-accent aspect-[2/3] rounded-lg bg-gradient-to-br" />
          {presentation.caption !== "artwork" ? (
            <>
              <div className="bg-foreground/75 mt-2 h-2 w-4/5 rounded-full" />
              {presentation.caption === "title_metadata" ? (
                <div className="bg-muted-foreground/45 mt-1.5 h-1.5 w-1/2 rounded-full" />
              ) : null}
            </>
          ) : null}
        </div>
      ))}
    </div>
  );
}

function InterfaceHeader() {
  return (
    <header className="space-y-2">
      <h2 className="text-2xl font-semibold tracking-tight sm:text-3xl">Navigation & cards</h2>
      <p className="text-muted-foreground text-sm">
        These choices sync between web browsers signed into this profile. TV, mobile, tablet, and
        desktop-native apps keep their own matching-device layouts.
      </p>
    </header>
  );
}

export default function InterfaceSettings() {
  const { data: libraries = [] } = useUserLibraries();
  const customization = useUICustomization();
  const setValue = useSetSettingValue();
  const clearValue = useClearSettingValue();
  const baselineMenu = useMemo(
    () => customization.primaryMenu ?? defaultWebPrimaryMenu(),
    [customization.primaryMenu],
  );
  const baselineKey = useMemo(() => JSON.stringify(baselineMenu.items), [baselineMenu.items]);
  const [menuDraft, setMenuDraft] = useState<{
    baselineKey: string;
    /** The just-saved baseline expected from the asynchronous query refresh. */
    pendingBaselineKey?: string;
    items: PrimaryMenuItem[];
    dirty: boolean;
  } | null>(null);
  const [addItemKey, setAddItemKey] = useState("");
  const menuDraftMatchesBaseline =
    menuDraft?.baselineKey === baselineKey || menuDraft?.pendingBaselineKey === baselineKey;
  const menuItems = menuDraftMatchesBaseline ? menuDraft.items : baselineMenu.items;
  const menuDirty = menuDraftMatchesBaseline === true && menuDraft.dirty;
  const cardPresentation = customization.cardPresentation;
  const cardDeviceOverride = customization.cardPresentationSource === "profile_device";
  const cardClientOverride = customization.cardPresentationSource === "profile_client";
  const menuDeviceOverride = customization.primaryMenuSource === "profile_device";
  const menuClientOverride = customization.primaryMenuSource === "profile_client";
  const menuMutationPending = setValue.isPending || clearValue.isPending;
  const cardMutationPending = menuMutationPending || cardDeviceOverride;
  const menuAtLimit = menuItems.length >= 64;

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
  const selectedAddItem = availableItems.find((item) => menuItemKey(item) === addItemKey);

  async function saveCardPresentation(next: CardPresentation) {
    try {
      await setValue.mutateAsync({
        key: SETTING_KEYS.UI_CARD_PRESENTATION,
        value: next,
        identity: CLIENT_SCOPE,
      });
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "Could not save card layout");
    }
  }

  async function resetCardPresentation() {
    try {
      await clearValue.mutateAsync({
        key: SETTING_KEYS.UI_CARD_PRESENTATION,
        identity: CLIENT_SCOPE,
      });
      toast.success("Web-family card layout reset");
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "Could not reset card layout");
    }
  }

  async function clearDeviceOverride(
    key: typeof SETTING_KEYS.UI_CARD_PRESENTATION | typeof SETTING_KEYS.NAV_PRIMARY_MENU,
    label: string,
  ) {
    try {
      await clearValue.mutateAsync({ key, identity: { scope: "profile_device" } });
      toast.success(`${label} now follows the web-family preference`);
    } catch (error) {
      toast.error(
        error instanceof Error ? error.message : `Could not clear ${label.toLowerCase()}`,
      );
    }
  }

  function updateMenu(next: PrimaryMenuItem[]) {
    setMenuDraft((current) => {
      if (current?.pendingBaselineKey === baselineKey) {
        return { baselineKey, items: next, dirty: true };
      }
      if (current?.baselineKey === baselineKey) {
        return { ...current, items: next, dirty: true };
      }
      return { baselineKey, items: next, dirty: true };
    });
  }

  async function saveMenu() {
    const savedItems = menuItems;
    const savedBaselineKey = JSON.stringify(savedItems);
    try {
      await setValue.mutateAsync({
        key: SETTING_KEYS.NAV_PRIMARY_MENU,
        value: { items: savedItems },
        identity: CLIENT_SCOPE,
      });
      setMenuDraft((current) => {
        const currentItemsKey = current ? JSON.stringify(current.items) : null;
        if (current?.dirty && currentItemsKey !== savedBaselineKey) {
          return { ...current, pendingBaselineKey: savedBaselineKey };
        }
        return {
          baselineKey,
          pendingBaselineKey: savedBaselineKey,
          items: savedItems,
          dirty: false,
        };
      });
      toast.success("Web navigation saved");
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "Could not save navigation");
    }
  }

  async function resetMenu() {
    const pendingClientOverride = menuDraft?.pendingBaselineKey !== undefined;
    if (!menuClientOverride && !pendingClientOverride) {
      setMenuDraft(null);
      toast.success("Web navigation reset");
      return;
    }
    try {
      await clearValue.mutateAsync({
        key: SETTING_KEYS.NAV_PRIMARY_MENU,
        identity: CLIENT_SCOPE,
      });
      setMenuDraft(null);
      toast.success("Web navigation reset");
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "Could not reset navigation");
    }
  }

  if (customization.isLoading) {
    return (
      <div className="space-y-6">
        <InterfaceHeader />
        <div className="surface-panel-subtle text-muted-foreground rounded-xl border p-5 text-sm">
          Checking server support…
        </div>
      </div>
    );
  }

  if (customization.isUnavailable) {
    return (
      <div className="space-y-6">
        <InterfaceHeader />
        <div className="surface-panel-subtle rounded-xl border p-5" role="alert">
          <p className="font-medium">Customization unavailable</p>
          <p className="text-muted-foreground mt-1 text-sm">
            Saved navigation and card settings could not be loaded. Editing stays disabled to
            protect your existing choices.
          </p>
        </div>
      </div>
    );
  }

  if (!customization.isSupported) {
    return (
      <div className="space-y-6">
        <InterfaceHeader />
        <div className="surface-panel-subtle rounded-xl border p-5" role="alert">
          <p className="font-medium">Server upgrade required</p>
          <p className="text-muted-foreground mt-1 text-sm">
            This server does not support synchronized navigation and card customization yet.
          </p>
        </div>
      </div>
    );
  }

  return (
    <div className="space-y-6">
      <InterfaceHeader />

      <SettingsGroup
        title="Card preset"
        description="Start with a complete layout, then adjust poster size or captions below."
      >
        {cardDeviceOverride ? (
          <div className="border-border/70 bg-muted/25 mb-4 flex flex-col gap-3 rounded-xl border p-4 sm:flex-row sm:items-center sm:justify-between">
            <p className="text-muted-foreground text-sm">
              This browser has a higher-priority device override. Clear it before editing the
              preference shared by web browsers.
            </p>
            <Button
              type="button"
              variant="outline"
              disabled={menuMutationPending}
              onClick={() =>
                void clearDeviceOverride(SETTING_KEYS.UI_CARD_PRESENTATION, "Card layout")
              }
            >
              Use web-family layout
            </Button>
          </div>
        ) : null}
        <div className="grid gap-3 sm:grid-cols-2">
          {CARD_PRESENTATION_PRESETS.map((preset) => {
            const active = samePresentation(cardPresentation, preset.value);
            return (
              <button
                key={preset.id}
                type="button"
                onClick={() => void saveCardPresentation(preset.value)}
                aria-pressed={active}
                disabled={cardMutationPending}
                className={cn(
                  "surface-panel-subtle relative rounded-xl border p-4 text-left transition-colors",
                  active ? "border-primary/50 bg-primary/8" : "border-border/70 hover:bg-accent/45",
                  "disabled:cursor-not-allowed disabled:opacity-60",
                )}
              >
                <div className="flex items-start justify-between gap-3">
                  <div>
                    <p className="text-sm font-semibold">{preset.label}</p>
                    <p className="text-muted-foreground mt-1 text-xs leading-relaxed">
                      {preset.description}
                    </p>
                  </div>
                  {active ? <Check className="text-primary h-4 w-4 shrink-0" /> : null}
                </div>
              </button>
            );
          })}
        </div>
      </SettingsGroup>

      <SettingsGroup
        title="Poster cards"
        description="Fine-tune density and the rows shown below artwork. Changes apply immediately."
      >
        <div className="grid gap-5 lg:grid-cols-[minmax(0,1fr)_minmax(240px,0.8fr)]">
          <div className="space-y-5">
            <div className="space-y-2">
              <p className="text-sm font-medium">Poster size</p>
              <div className="flex flex-wrap gap-2" role="radiogroup" aria-label="Poster size">
                {(
                  [
                    ["compact", "Compact"],
                    ["standard", "Standard"],
                    ["large", "Large"],
                  ] as const
                ).map(([value, label]) => (
                  <Button
                    key={value}
                    type="button"
                    role="radio"
                    aria-checked={cardPresentation.poster_size === value}
                    size="sm"
                    variant={cardPresentation.poster_size === value ? "default" : "outline"}
                    disabled={cardMutationPending}
                    onClick={() =>
                      void saveCardPresentation({
                        ...cardPresentation,
                        poster_size: value as PosterSize,
                      })
                    }
                  >
                    {label}
                  </Button>
                ))}
              </div>
            </div>
            <div className="space-y-2">
              <p className="text-sm font-medium">Caption</p>
              <div className="flex flex-wrap gap-2" role="radiogroup" aria-label="Card caption">
                {(
                  [
                    ["title_metadata", "Title & metadata"],
                    ["title", "Title only"],
                    ["artwork", "Artwork only"],
                  ] as const
                ).map(([value, label]) => (
                  <Button
                    key={value}
                    type="button"
                    role="radio"
                    aria-checked={cardPresentation.caption === value}
                    size="sm"
                    variant={cardPresentation.caption === value ? "default" : "outline"}
                    disabled={cardMutationPending}
                    onClick={() =>
                      void saveCardPresentation({
                        ...cardPresentation,
                        caption: value as CardCaption,
                      })
                    }
                  >
                    {label}
                  </Button>
                ))}
              </div>
            </div>
          </div>
          <CardPreview presentation={cardPresentation} />
        </div>
        {cardClientOverride ? (
          <div className="border-border/70 mt-5 flex flex-col gap-3 border-t pt-4 sm:flex-row sm:items-center sm:justify-between">
            <p id="card-layout-reset-description" className="text-muted-foreground text-sm">
              Remove the layout shared by web browsers and inherit the profile or app default.
            </p>
            <Button
              type="button"
              variant="outline"
              disabled={menuMutationPending}
              aria-describedby="card-layout-reset-description"
              onClick={() => void resetCardPresentation()}
            >
              <RotateCcw className="mr-1.5 h-4 w-4" />
              Reset web-family card layout
            </Button>
          </div>
        ) : null}
      </SettingsGroup>

      <SettingsGroup
        title="Primary menu"
        description="Choose the ordered shortcuts at the top of the web menu. Home stays available; search and profile controls are fixed."
      >
        <div className="space-y-4">
          <div className="text-muted-foreground flex items-center gap-2 text-xs">
            <Monitor className="h-4 w-4" />
            Web browsers
          </div>
          {menuDeviceOverride ? (
            <div className="border-border/70 bg-muted/25 flex flex-col gap-3 rounded-xl border p-4 sm:flex-row sm:items-center sm:justify-between">
              <p className="text-muted-foreground text-sm">
                This browser has a higher-priority device menu. Clear it before editing the menu
                shared by web browsers.
              </p>
              <Button
                type="button"
                variant="outline"
                disabled={menuMutationPending}
                onClick={() =>
                  void clearDeviceOverride(SETTING_KEYS.NAV_PRIMARY_MENU, "Navigation")
                }
              >
                Use web-family menu
              </Button>
            </div>
          ) : null}
          <ol className="space-y-2">
            {menuItems.map((item, index) => {
              const home = item.type === "builtin" && item.destination === "home";
              return (
                <li
                  key={menuItemKey(item)}
                  className="surface-panel-subtle border-border/65 flex items-center gap-3 rounded-xl border px-3 py-2.5"
                >
                  <span className="text-muted-foreground w-6 shrink-0 text-center text-xs tabular-nums">
                    {index + 1}
                  </span>
                  <span className="min-w-0 flex-1 truncate text-sm font-medium">
                    {menuItemLabel(item)}
                  </span>
                  <div className="flex shrink-0 items-center gap-1">
                    <Button
                      type="button"
                      variant="ghost"
                      size="icon"
                      disabled={menuMutationPending || menuDeviceOverride || index === 0}
                      aria-label={`Move ${menuItemLabel(item)} up`}
                      onClick={() => updateMenu(moveMenuItem(menuItems, index, -1))}
                    >
                      <ArrowUp className="h-4 w-4" />
                    </Button>
                    <Button
                      type="button"
                      variant="ghost"
                      size="icon"
                      disabled={
                        menuMutationPending || menuDeviceOverride || index === menuItems.length - 1
                      }
                      aria-label={`Move ${menuItemLabel(item)} down`}
                      onClick={() => updateMenu(moveMenuItem(menuItems, index, 1))}
                    >
                      <ArrowDown className="h-4 w-4" />
                    </Button>
                    <Button
                      type="button"
                      variant="ghost"
                      size="icon"
                      disabled={menuMutationPending || menuDeviceOverride || home}
                      aria-label={home ? "Home cannot be removed" : `Remove ${menuItemLabel(item)}`}
                      onClick={() =>
                        updateMenu(menuItems.filter((_, itemIndex) => itemIndex !== index))
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
            <div className="flex flex-col gap-2 sm:flex-row sm:items-center">
              <Select
                value={addItemKey}
                onValueChange={setAddItemKey}
                disabled={menuMutationPending || menuDeviceOverride || menuAtLimit}
              >
                <SelectTrigger className="w-full sm:max-w-sm">
                  <SelectValue placeholder="Choose destination or shortcut" />
                </SelectTrigger>
                <SelectContent>
                  {availableItems.map((item) => (
                    <SelectItem key={menuItemKey(item)} value={menuItemKey(item)}>
                      {menuItemLabel(item)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              <Button
                type="button"
                variant="secondary"
                disabled={
                  !selectedAddItem || menuMutationPending || menuDeviceOverride || menuAtLimit
                }
                onClick={() => {
                  if (!selectedAddItem) return;
                  updateMenu([...menuItems, selectedAddItem]);
                  setAddItemKey("");
                }}
              >
                Add to menu
              </Button>
            </div>
          ) : null}

          <div className="flex flex-wrap gap-2 pt-1">
            <Button
              type="button"
              onClick={() => void saveMenu()}
              disabled={!menuDirty || menuMutationPending || menuDeviceOverride}
            >
              Save menu
            </Button>
            <Button
              type="button"
              variant="outline"
              onClick={() => void resetMenu()}
              disabled={menuMutationPending || menuDeviceOverride}
            >
              <RotateCcw className="mr-1.5 h-4 w-4" />
              Reset to default
            </Button>
          </div>
        </div>
      </SettingsGroup>
    </div>
  );
}
