import { useEffect, useMemo, useState } from "react";
import { Check, Plus } from "lucide-react";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { cn } from "@/lib/utils";

import { replacedShort, type AddableCategory } from "./levels";

/**
 * "Add a setting" for one level: a searchable list of the settings the level
 * can hold, by category. Each shows the value it would replace; ones the level
 * already stores are listed but disabled.
 */
export function AddSettingPicker({
  label,
  categories,
  disabled,
  onAdd,
}: {
  label: string;
  categories: AddableCategory[];
  disabled?: boolean;
  /** Resolves once the settings are written; the picker closes then. */
  onAdd: (keys: string[]) => Promise<void>;
}) {
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [chosen, setChosen] = useState<string[]>([]);
  const [saving, setSaving] = useState(false);

  const visible = useMemo(() => {
    const needle = query.trim().toLowerCase();
    if (!needle) return categories;
    return categories
      .map((group) => ({
        ...group,
        settings: group.settings.filter((setting) =>
          [setting.label, setting.description, group.category].some((text) =>
            text.toLowerCase().includes(needle),
          ),
        ),
      }))
      .filter((group) => group.settings.length > 0);
  }, [categories, query]);

  // After a partial failure, settings that did land turn "set here" once the
  // list refreshes; drop them from the selection so a retry adds only the rest.
  useEffect(() => {
    const stored = new Set(
      categories.flatMap((group) => group.settings.filter((s) => s.setHere).map((s) => s.key)),
    );
    setChosen((current) =>
      current.some((key) => stored.has(key)) ? current.filter((key) => !stored.has(key)) : current,
    );
  }, [categories]);

  const reset = () => {
    setQuery("");
    setChosen([]);
  };
  const toggle = (key: string) =>
    setChosen((current) =>
      current.includes(key) ? current.filter((k) => k !== key) : [...current, key],
    );
  const add = async () => {
    setSaving(true);
    try {
      await onAdd(chosen);
      setOpen(false);
      reset();
    } catch {
      // The mutation's own toast reports the failure; keep the picker open.
    } finally {
      setSaving(false);
    }
  };

  return (
    <Popover
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        if (!next) reset();
      }}
    >
      <PopoverTrigger asChild>
        <Button variant="ghost" size="sm" disabled={disabled}>
          <Plus className="h-3.5 w-3.5" />
          {label}
        </Button>
      </PopoverTrigger>
      <PopoverContent className="flex w-[min(24rem,calc(100vw-2rem))] flex-col overflow-hidden">
        <div className="border-border/70 border-b p-2.5">
          <Input
            type="search"
            autoFocus
            aria-label="Search settings"
            placeholder="Search settings"
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            className="h-8"
          />
        </div>
        <div className="max-h-80 overflow-y-auto p-1.5">
          {visible.length === 0 ? (
            <p className="text-muted-foreground px-3 py-6 text-center text-sm">
              No settings match.
            </p>
          ) : (
            visible.map((group) => (
              <div key={group.category} role="group" aria-label={group.category}>
                <div className="text-muted-foreground flex items-center justify-between px-2.5 pt-2.5 pb-1 text-[11px] font-semibold tracking-[0.06em] uppercase">
                  <span>{group.category}</span>
                  <span className="tabular-nums">
                    {query.trim()
                      ? `${group.settings.length} ${group.settings.length === 1 ? "match" : "matches"}`
                      : group.settings.length}
                  </span>
                </div>
                {group.settings.map((setting) => {
                  const selected = chosen.includes(setting.key);
                  return (
                    <button
                      key={setting.key}
                      type="button"
                      role="checkbox"
                      aria-checked={selected}
                      disabled={setting.setHere}
                      title={setting.description}
                      onClick={() => toggle(setting.key)}
                      className={cn(
                        "flex w-full items-center gap-2 rounded-md px-2.5 py-1.5 text-left text-sm transition-colors",
                        "hover:bg-accent/60 focus-visible:bg-accent/60 outline-none disabled:pointer-events-none disabled:opacity-50",
                        selected && "bg-accent",
                      )}
                    >
                      <Check
                        aria-hidden="true"
                        className={cn("h-3.5 w-3.5 shrink-0", !selected && "invisible")}
                      />
                      <span className="min-w-0 flex-1 truncate">{setting.label}</span>
                      <span className="text-muted-foreground max-w-[45%] shrink-0 truncate text-xs">
                        {setting.setHere ? "set here" : replacedShort(setting.replaced)}
                      </span>
                    </button>
                  );
                })}
              </div>
            ))
          )}
        </div>
        <div className="border-border/70 flex items-center justify-end gap-2 border-t p-2.5">
          <Button variant="ghost" size="sm" onClick={() => setOpen(false)}>
            Cancel
          </Button>
          <Button size="sm" disabled={chosen.length === 0 || saving} onClick={() => void add()}>
            {chosen.length > 1 ? `Add ${chosen.length} settings` : "Add setting"}
          </Button>
        </div>
      </PopoverContent>
    </Popover>
  );
}
