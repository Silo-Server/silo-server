import { Component, type ErrorInfo, type ReactNode } from "react";

interface LocalErrorBoundaryProps {
  children: ReactNode;
  /** Rendered in place of the children once they throw. Defaults to nothing. */
  fallback?: ReactNode | ((error: unknown) => ReactNode);
  /** Called once per caught error, after the fallback commits. */
  onError?: (error: unknown) => void;
}

interface LocalErrorBoundaryState {
  failed: boolean;
  error: unknown;
}

/**
 * Keeps a failure inside an optional piece of UI from reaching the app-level
 * ErrorBoundary, which replaces the whole page. Lazy components rendered
 * outside the routes need one: React.lazy keeps a failed chunk import failed,
 * so without it a single missing chunk breaks every page that renders the
 * component until the tab reloads.
 */
export class LocalErrorBoundary extends Component<
  LocalErrorBoundaryProps,
  LocalErrorBoundaryState
> {
  state: LocalErrorBoundaryState = { failed: false, error: undefined };

  static getDerivedStateFromError(error: unknown): LocalErrorBoundaryState {
    return { failed: true, error };
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error("[LocalErrorBoundary]", error, info.componentStack);
    this.props.onError?.(error);
  }

  render() {
    if (!this.state.failed) return this.props.children;
    const { fallback = null } = this.props;
    return typeof fallback === "function" ? fallback(this.state.error) : fallback;
  }
}
