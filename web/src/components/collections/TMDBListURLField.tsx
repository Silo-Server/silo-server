import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { isValidTMDBListURL, TMDB_LIST_URL_PLACEHOLDER } from "@/lib/tmdbList";

interface TMDBListURLFieldProps {
  id: string;
  value: string;
  onChange: (value: string) => void;
  disabled?: boolean;
}

/**
 * URL input for a public TMDB list. TMDB offers no list search, so the list is
 * picked on themoviedb.org and pasted here.
 */
export function TMDBListURLField({ id, value, onChange, disabled }: TMDBListURLFieldProps) {
  const invalid = value.trim().length > 0 && !isValidTMDBListURL(value);
  const hintId = `${id}-hint`;
  return (
    <div className="space-y-2">
      <Label htmlFor={id}>TMDB list URL</Label>
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
          ? "Enter a TMDB list URL such as https://www.themoviedb.org/list/310, or the list's number."
          : "Paste a public list from themoviedb.org. Movies and shows sync in list order and match your libraries by TMDB, IMDb, or TVDB ID."}
      </p>
    </div>
  );
}
