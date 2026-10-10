import { useState } from "react";

import { Button } from "@/components/ui/button";

/**
 * Shown when a save answered 412: someone else changed the record since this
 * editor read it. The admin's edits stay on screen; only an explicit reload
 * replaces them with the latest version and its validator.
 */
export function EditorConflict({ onReload }: { onReload: () => Promise<void> }) {
  const [loading, setLoading] = useState(false);
  return (
    <div role="alert" className="space-y-2 text-sm">
      <p>
        This was changed by another administrator. Your edits have been kept. Reload to review the
        latest version before saving again.
      </p>
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
        Reload latest version
      </Button>
    </div>
  );
}
