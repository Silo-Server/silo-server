import { Component, type ErrorInfo, type ReactNode } from "react";
import { useLocation } from "react-router";

interface ErrorBoundaryProps {
  children: ReactNode;
  resetKeys?: unknown[];
}

interface ErrorBoundaryState {
  hasError: boolean;
  prevResetKeys: unknown[];
}

class ErrorBoundaryInner extends Component<ErrorBoundaryProps, ErrorBoundaryState> {
  constructor(props: ErrorBoundaryProps) {
    super(props);
    this.state = { hasError: false, prevResetKeys: props.resetKeys ?? [] };
  }

  static getDerivedStateFromError(): Partial<ErrorBoundaryState> {
    return { hasError: true };
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
        <div className="bg-background flex min-h-screen items-center justify-center p-8">
          <div className="max-w-md text-center">
            <h1 className="text-foreground mb-4 text-2xl font-bold">Something went wrong</h1>
            <p className="text-muted-foreground mb-6">
              An unexpected error occurred. Try refreshing the page.
            </p>
            <div className="flex items-center justify-center gap-3">
              <button
                type="button"
                onClick={() => this.setState({ hasError: false })}
                className="bg-primary text-primary-foreground hover:bg-primary/90 rounded-md px-4 py-2"
              >
                Try again
              </button>
              <button
                type="button"
                onClick={() => window.location.reload()}
                className="border-border hover:bg-muted/40 rounded-md border px-4 py-2"
              >
                Refresh Page
              </button>
            </div>
          </div>
        </div>
      );
    }

    return this.props.children;
  }
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
