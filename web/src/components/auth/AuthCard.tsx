import type { ReactNode } from "react";
import { Link } from "react-router";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { AuthBackground } from "@/components/auth/AuthBackground";

/** The signed-out account screens' card (password reset and its request). */
export function AuthCard({
  eyebrow,
  title,
  description,
  children,
}: {
  eyebrow?: string;
  title: string;
  description?: string;
  children: ReactNode;
}) {
  return (
    <div className="auth-shell">
      <AuthBackground />
      <Card className="auth-card glass panel-border w-full max-w-sm border-0">
        <CardHeader>
          {eyebrow && (
            <p className="text-muted-foreground font-mono text-[11px] font-semibold tracking-[0.1em] uppercase">
              {eyebrow}
            </p>
          )}
          <CardTitle className="text-3xl font-extrabold tracking-[-0.04em]">{title}</CardTitle>
          {description && (
            <CardDescription className="mt-2 text-sm leading-6">{description}</CardDescription>
          )}
        </CardHeader>
        <CardContent>{children}</CardContent>
      </Card>
    </div>
  );
}

export function SignInHint() {
  return (
    <p className="text-muted-foreground mt-4 text-center text-sm">
      Remembered it?{" "}
      <Link to="/login" className="text-foreground underline hover:no-underline">
        Sign in
      </Link>
    </p>
  );
}
