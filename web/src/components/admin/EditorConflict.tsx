import { useState } from "react";

import { Button } from "@/components/ui/button";
import { useT } from "@/i18n";

/**
 * Shown when a save answered 412: someone else changed the record since this
 * editor read it. The admin's edits stay on screen; only an explicit reload
 * replaces them with the latest version and its validator.
 */
export function EditorConflict({ onReload }: { onReload: () => Promise<void> }) {
  const t = useT();
  const [loading, setLoading] = useState(false);
  return (
    <div role="alert" className="space-y-2 text-sm">
      <p>{t("web.admin.common.editorConflict.message")}</p>
      <Button
        type="button"
        variant="outline"
        size="sm"
        disabled={loading}
        onClick={async () => {
          setLoading(true);
          try {
            await onReload();
          } finally {
            setLoading(false);
          }
        }}
      >
        {t("web.admin.common.editorConflict.reload")}
      </Button>
    </div>
  );
}
