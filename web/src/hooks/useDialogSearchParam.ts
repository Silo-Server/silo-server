import { useCallback } from "react";
import { useSearchParams } from "react-router";

/**
 * A dialog a link can open: `?dialog=<name>`. Opening and closing replace the
 * history entry, so Back leaves the page rather than reopening a closed dialog.
 */
export function useDialogSearchParam(name: string): [boolean, (open: boolean) => void] {
  const [searchParams, setSearchParams] = useSearchParams();
  const setOpen = useCallback(
    (open: boolean) =>
      setSearchParams(
        (current) => {
          const next = new URLSearchParams(current);
          if (open) next.set("dialog", name);
          else next.delete("dialog");
          return next;
        },
        { replace: true },
      ),
    [name, setSearchParams],
  );
  return [searchParams.get("dialog") === name, setOpen];
}
