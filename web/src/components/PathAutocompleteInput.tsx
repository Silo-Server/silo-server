import { useMemo } from "react";

import { useFilesystemBrowseWhen } from "@/hooks/queries/admin/libraries";
import { useDebounce } from "@/hooks/useDebounce";
import { cn } from "@/lib/utils";
import { Input } from "@/components/ui/input";

const ROOT_PATH = "/";

interface AutocompleteContext {
  browsePath: string;
  fragment: string;
}

function getAutocompleteContext(path: string): AutocompleteContext | null {
  const trimmed = path.trim();
  if (!trimmed.startsWith(ROOT_PATH)) {
    return null;
  }

  if (trimmed === ROOT_PATH) {
    return { browsePath: ROOT_PATH, fragment: "" };
  }

  const endsWithSlash = trimmed.endsWith(ROOT_PATH);
  const normalized =
    endsWithSlash && trimmed.length > 1 ? trimmed.slice(0, trimmed.length - 1) : trimmed;
  const lastSlashIndex = normalized.lastIndexOf(ROOT_PATH);

  if (lastSlashIndex < 0) {
    return null;
  }

  if (endsWithSlash) {
    return { browsePath: normalized || ROOT_PATH, fragment: "" };
  }

  return {
    browsePath: lastSlashIndex === 0 ? ROOT_PATH : normalized.slice(0, lastSlashIndex),
    fragment: normalized.slice(lastSlashIndex + 1),
  };
}

type PathAutocompleteInputProps = Omit<React.ComponentProps<"input">, "value" | "onChange"> & {
  value: string;
  onValueChange: (value: string) => void;
};

export default function PathAutocompleteInput({
  value,
  onValueChange,
  className,
  onKeyDown,
  onKeyDownCapture,
  ...props
}: PathAutocompleteInputProps) {
  const debouncedValue = useDebounce(value.trim(), 200);
  const autocompleteContext = useMemo(
    () => getAutocompleteContext(debouncedValue),
    [debouncedValue],
  );
  const autocompleteBrowse = useFilesystemBrowseWhen(
    autocompleteContext?.browsePath ?? "",
    !!autocompleteContext,
    autocompleteContext?.fragment ?? "",
  );

  const autocompleteSuggestions = useMemo(() => {
    const entries = autocompleteBrowse.data?.entries ?? [];
    const fragment = autocompleteContext?.fragment.trim().toLowerCase() ?? "";

    return entries.filter((entry) =>
      fragment.length === 0 ? true : entry.name.toLowerCase().startsWith(fragment),
    );
  }, [autocompleteBrowse.data?.entries, autocompleteContext?.fragment]);

  const activeAutocompleteSuggestion = autocompleteSuggestions[0];
  const trimmedValue = value.trim();
  const completionSuffix =
    activeAutocompleteSuggestion &&
    activeAutocompleteSuggestion.path !== trimmedValue &&
    activeAutocompleteSuggestion.path.startsWith(trimmedValue)
      ? activeAutocompleteSuggestion.path.slice(trimmedValue.length)
      : "";
  const canAcceptAutocomplete =
    completionSuffix.length > 0 && activeAutocompleteSuggestion !== undefined;

  function acceptAutocomplete() {
    if (!activeAutocompleteSuggestion) {
      return;
    }

    onValueChange(activeAutocompleteSuggestion.path);
  }

  return (
    <div className="relative w-full min-w-0 flex-1">
      {completionSuffix ? (
        <div className="text-muted-foreground pointer-events-none absolute inset-0 flex items-center px-3 text-sm">
          <span className="invisible">{value}</span>
          <span>{completionSuffix}</span>
        </div>
      ) : null}
      <Input
        {...props}
        value={value}
        className={cn("relative bg-transparent", className)}
        onChange={(event) => {
          onValueChange(event.target.value);
        }}
        onKeyDownCapture={(event) => {
          if (!event.shiftKey && event.key === "Tab" && canAcceptAutocomplete) {
            event.preventDefault();
            acceptAutocomplete();
            return;
          }

          onKeyDownCapture?.(event);
        }}
        onKeyDown={(event) => {
          if (!event.shiftKey && event.key === "Tab" && canAcceptAutocomplete) {
            event.preventDefault();
            acceptAutocomplete();
            return;
          }

          onKeyDown?.(event);
        }}
      />
    </div>
  );
}
