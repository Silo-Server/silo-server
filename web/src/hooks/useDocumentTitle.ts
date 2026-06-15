import { useEffect } from "react";

import {
  APP_DOCUMENT_TITLE,
  formatDocumentTitle,
  setActiveDocumentTitleLabel,
} from "@/lib/documentTitle";

export function useDocumentTitle(label?: string | null) {
  useEffect(() => {
    if (typeof document === "undefined") {
      return;
    }

    setActiveDocumentTitleLabel(label);
    document.title = formatDocumentTitle(label);

    return () => {
      setActiveDocumentTitleLabel(null);
      document.title = APP_DOCUMENT_TITLE;
    };
  }, [label]);
}
