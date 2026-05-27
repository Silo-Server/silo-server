import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { cn } from "@/lib/utils";
import { LANGUAGES } from "@/player/utils/languageNames";
import { SUBTITLE_PROVIDER_OPTIONS } from "./subtitleAdminStyles";

const ALL = "all";

interface AdminSubtitlesFiltersProps {
  provider: string;
  language: string;
  userId: string;
  search: string;
  users: Array<{ id: number; username: string }>;
  onProviderChange: (value: string) => void;
  onLanguageChange: (value: string) => void;
  onUserChange: (value: string) => void;
  onSearchChange: (value: string) => void;
  onReset: () => void;
}

export default function AdminSubtitlesFilters({
  provider,
  language,
  userId,
  search,
  users,
  onProviderChange,
  onLanguageChange,
  onUserChange,
  onSearchChange,
  onReset,
}: AdminSubtitlesFiltersProps) {
  return (
    <div className="surface-panel rounded-2xl border-0 px-3 py-3 sm:px-4">
      <div className="flex flex-col gap-4 xl:flex-row xl:items-center xl:justify-between">
        <div className="min-w-0 flex-1">
          <Input
            value={search}
            onChange={(event) => onSearchChange(event.target.value)}
            placeholder="Search release name…"
            className="font-mono text-xs sm:max-w-sm"
            aria-label="Search subtitle release name"
          />
        </div>

        <div
          className="flex flex-wrap items-center gap-1.5"
          role="group"
          aria-label="Filter by subtitle provider"
        >
          {SUBTITLE_PROVIDER_OPTIONS.map((option) => {
            const active = provider === option.value;
            return (
              <button
                key={option.value}
                type="button"
                aria-pressed={active}
                onClick={() => onProviderChange(option.value)}
                className={cn(
                  "rounded-full border px-3 py-1.5 text-xs font-medium transition-colors",
                  active
                    ? "border-primary/40 bg-primary/15 text-foreground shadow-[inset_0_1px_0_rgb(255_255_255_/_0.06)]"
                    : "border-border/60 bg-muted/20 text-muted-foreground hover:bg-muted/40 hover:text-foreground",
                )}
              >
                {option.label}
              </button>
            );
          })}
        </div>

        <div className="flex flex-wrap items-center gap-2">
          <Select value={language} onValueChange={onLanguageChange}>
            <SelectTrigger className="w-[180px]" aria-label="Filter by language">
              <SelectValue placeholder="Language" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={ALL}>All languages</SelectItem>
              {LANGUAGES.map((lang) => (
                <SelectItem key={lang.code} value={lang.code}>
                  {lang.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>

          <Select value={userId} onValueChange={onUserChange}>
            <SelectTrigger className="w-[200px]" aria-label="Filter by uploader">
              <SelectValue placeholder="Uploader" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={ALL}>All uploaders</SelectItem>
              {users.map((user) => (
                <SelectItem key={user.id} value={String(user.id)}>
                  {user.username}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>

          <Button type="button" variant="ghost" size="sm" onClick={onReset}>
            Reset filters
          </Button>
        </div>
      </div>
    </div>
  );
}

export { ALL as FILTER_ALL };
