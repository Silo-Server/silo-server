import { Component, type ErrorInfo, type ReactNode } from "react";
import { useLocation } from "react-router";

import { useT } from "@/i18n";

interface ErrorBoundaryProps {
  children: ReactNode;
  resetKeys?: unknown[];
}

interface ErrorBoundaryState {
  hasError: boolean;
  chunkLoadFailed: boolean;
  prevResetKeys: unknown[];
}

// A route's code failed to download: the browser's failed dynamic import
// (Chromium and Firefox say "dynamically imported module", Safari "Importing a
// module script failed") or Vite's preload of the chunk's CSS. React keeps a
// failed lazy import failed and browsers remember a failed module fetch for the
// life of the page, so only a reload can recover.
const CHUNK_LOAD_ERROR =
  /dynamically imported module|Importing a module script failed|Unable to preload CSS/i;

function isChunkLoadError(error: unknown): boolean {
  return error instanceof Error && CHUNK_LOAD_ERROR.test(error.message);
}

class ErrorBoundaryInner extends Component<ErrorBoundaryProps, ErrorBoundaryState> {
  constructor(props: ErrorBoundaryProps) {
    super(props);
    this.state = { hasError: false, chunkLoadFailed: false, prevResetKeys: props.resetKeys ?? [] };
  }

  static getDerivedStateFromError(error: unknown): Partial<ErrorBoundaryState> {
    return { hasError: true, chunkLoadFailed: isChunkLoadError(error) };
  }

  static getDerivedStateFromProps(
    props: ErrorBoundaryProps,
    state: ErrorBoundaryState,
  ): Partial<ErrorBoundaryState> | null {
    const nextKeys = props.resetKeys ?? [];
    if (
      state.hasError &&
      nextKeys.length === state.prevResetKeys.length &&
      nextKeys.every((key, i) => key === state.prevResetKeys[i])
    ) {
      return null;
    }
    if (state.hasError) {
      return { hasError: false, prevResetKeys: nextKeys };
    }
    if (
      nextKeys.length !== state.prevResetKeys.length ||
      nextKeys.some((key, i) => key !== state.prevResetKeys[i])
    ) {
      return { prevResetKeys: nextKeys };
    }
    return null;
  }

  override componentDidCatch(error: Error, info: ErrorInfo) {
    console.error("[ErrorBoundary]", error, info.componentStack);
  }

  override render() {
    if (this.state.hasError) {
      return (
        <ErrorFallback
          onRetry={
            this.state.chunkLoadFailed ? undefined : () => this.setState({ hasError: false })
          }
        />
      );
    }

    return this.props.children;
  }
}

const PRIMARY_BUTTON =
  "bg-primary text-primary-foreground hover:bg-primary-hover rounded-md px-4 py-2";
const SECONDARY_BUTTON = "border-border hover:bg-muted/40 rounded-md border px-4 py-2";

/**
 * The full-page error screen; a function component so its text follows the language.
 * Without `onRetry` it offers only the reload.
 */
function ErrorFallback({ onRetry }: { onRetry?: () => void }) {
  const t = useT();
  return (
    <div className="bg-background flex min-h-screen items-center justify-center p-8">
      <div className="max-w-md text-center">
        <h1 className="text-foreground mb-4 text-2xl font-bold">
          {t("web.shell.errorBoundary.title")}
        </h1>
        <p className="text-muted-foreground mb-6">{t("web.shell.errorBoundary.description")}</p>
        <div className="flex items-center justify-center gap-3">
          {onRetry && (
            <button type="button" onClick={onRetry} className={PRIMARY_BUTTON}>
              {t("common.tryAgain")}
            </button>
          )}
          <button
            type="button"
            onClick={() => window.location.reload()}
            className={onRetry ? SECONDARY_BUTTON : PRIMARY_BUTTON}
          >
            {t("web.shell.errorBoundary.refresh")}
          </button>
        </div>
      </div>
    </div>
  );
}

/** Wrapper that passes location.pathname as a resetKey so errors clear on navigation. */
export function ErrorBoundary({ children, resetKeys = [], ...rest }: ErrorBoundaryProps) {
  const location = useLocation();
  return (
    <ErrorBoundaryInner resetKeys={[location.pathname, ...resetKeys]} {...rest}>
      {children}
    </ErrorBoundaryInner>
  );
}
