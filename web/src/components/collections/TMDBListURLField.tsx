import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { isValidTMDBListURL, TMDB_LIST_URL_PLACEHOLDER } from "@/lib/tmdbList";
import { useT } from "@/i18n";

interface TMDBListURLFieldProps {
  id: string;
  value: string;
  onChange: (value: string) => void;
  disabled?: boolean;
  label?: string;
  /** The line under the field while the link is valid or empty. */
  help?: string;
}

/**
 * URL input for a public TMDB list. TMDB offers no list search, so the list is
 * picked on themoviedb.org and pasted here.
 */
export function TMDBListURLField({
  id,
  value,
  onChange,
  disabled,
  label,
  help,
}: TMDBListURLFieldProps) {
  const t = useT();
  const invalid = value.trim().length > 0 && !isValidTMDBListURL(value);
  const hintId = `${id}-hint`;
  return (
    <div className="space-y-2">
      <Label htmlFor={id}>{label ?? t("web.collections.tmdbListUrl.label")}</Label>
      <Input
        id={id}
        value={value}
        onChange={(event) => onChange(event.target.value)}
        placeholder={TMDB_LIST_URL_PLACEHOLDER}
        aria-invalid={invalid || undefined}
        aria-describedby={hintId}
        disabled={disabled}
        required
      />
      <p
        id={hintId}
        className={invalid ? "text-destructive text-xs" : "text-muted-foreground text-xs"}
      >
        {invalid
          ? t("web.collections.tmdbListUrl.invalid")
          : (help ?? t("web.collections.tmdbListUrl.help"))}
      </p>
    </div>
  );
}
