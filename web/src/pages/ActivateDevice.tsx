import { useCallback, useEffect, useMemo, useState } from "react";
import type { FormEvent } from "react";
import { Link, useSearchParams } from "react-router";
import { api } from "@/api/client";
import type { DeviceLoginLookupResponse } from "@/api/types";
import { useAuth } from "@/hooks/useAuth";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { useDocumentTitle } from "@/hooks/useDocumentTitle";
import { useServerBranding } from "@/hooks/useServerBranding";
import { AuthBackground } from "@/components/auth/AuthBackground";
import { toast } from "sonner";

function normalizeCode(value: string) {
  const clean = value
    .toUpperCase()
    .replace(/[^A-Z0-9]/g, "")
    .slice(0, 8);
  if (clean.length <= 4) {
    return clean;
  }
  return `${clean.slice(0, 4)}-${clean.slice(4)}`;
}

export default function ActivateDevice() {
  const { user, loading, setupLoading } = useAuth();
  const [searchParams, setSearchParams] = useSearchParams();
  const [codeInput, setCodeInput] = useState(searchParams.get("code") ?? "");
  const [details, setDetails] = useState<DeviceLoginLookupResponse | null>(null);
  const [loadingDetails, setLoadingDetails] = useState(false);
  const [acting, setActing] = useState(false);
  const { serverName } = useServerBranding();

  useDocumentTitle("Approve Device");

  const token = searchParams.get("token") ?? "";
  const code = searchParams.get("code") ?? "";

  const redirectTarget = useMemo(() => {
    const query = searchParams.toString();
    return query ? `/activate?${query}` : "/activate";
  }, [searchParams]);

  const loadDetails = useCallback(async () => {
    if (!token && !code) {
      setDetails(null);
      return;
    }

    setLoadingDetails(true);
    try {
      const params = new URLSearchParams();
      if (token) {
        params.set("token", token);
      } else {
        params.set("code", code);
      }
      const result = await api<DeviceLoginLookupResponse>(`/auth/device?${params.toString()}`);
      setDetails(result);
    } catch (error) {
      setDetails(null);
      toast.error(error instanceof Error ? error.message : "Device request not found");
    } finally {
      setLoadingDetails(false);
    }
  }, [code, token]);

  useEffect(() => {
    void loadDetails();
  }, [loadDetails]);

  async function handleDecision(action: "approve" | "deny") {
    setActing(true);
    try {
      await api(`/auth/device/${action}`, {
        method: "POST",
        body: JSON.stringify({ token, code }),
      });
      await loadDetails();
    } catch (error) {
      toast.error(error instanceof Error ? error.message : `Failed to ${action} request`);
    } finally {
      setActing(false);
    }
  }

  function handleCodeSubmit(e: FormEvent) {
    e.preventDefault();
    const normalized = normalizeCode(codeInput);
    if (!normalized) {
      return;
    }
    setSearchParams({ code: normalized });
  }

  const loginHref = `/login?redirect=${encodeURIComponent(redirectTarget)}`;

  return (
    <div className="auth-shell">
      <AuthBackground />
      <Card className="auth-card glass panel-border w-full max-w-md border-0">
        <CardHeader>
          <CardTitle className="text-3xl font-extrabold tracking-[-0.04em]">{serverName}</CardTitle>
          <CardDescription className="mt-2 text-sm leading-6">
            Approve sign-in for the device you&apos;re trying to use.
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-6">
          {!token && !code ? (
            <form onSubmit={handleCodeSubmit} className="space-y-4">
              <div className="space-y-2">
                <Label htmlFor="device-code">Enter the code from your screen</Label>
                <Input
                  id="device-code"
                  value={codeInput}
                  onChange={(e) => setCodeInput(normalizeCode(e.target.value))}
                  autoCapitalize="characters"
                  autoComplete="off"
                  autoCorrect="off"
                  placeholder="ABCD-EFGH"
                />
              </div>
              <Button type="submit" className="w-full">
                Continue
              </Button>
            </form>
          ) : loadingDetails || loading || setupLoading ? (
            <div className="text-muted-foreground text-sm">Loading device request...</div>
          ) : !details ? (
            <div className="space-y-4">
              <p className="text-sm">That sign-in request could not be found.</p>
              <Button
                type="button"
                variant="outline"
                className="w-full"
                onClick={() => setSearchParams({})}
              >
                Enter another code
              </Button>
            </div>
          ) : (
            <div className="space-y-4">
              <div className="border-border/60 bg-background/50 rounded-md border p-4">
                <div className="space-y-1">
                  <div className="text-lg font-semibold">
                    {details.device_name || "This device"}
                  </div>
                  {details.device_platform ? (
                    <div className="text-muted-foreground text-sm">{details.device_platform}</div>
                  ) : null}
                  {details.ip_address_hint ? (
                    <div className="text-muted-foreground text-sm">{details.ip_address_hint}</div>
                  ) : null}
                </div>
                {details.match_code ? (
                  <div className="mt-4">
                    <div className="text-muted-foreground text-xs tracking-[0.12em] uppercase">
                      Match code
                    </div>
                    <div className="text-lg font-semibold">{details.match_code}</div>
                  </div>
                ) : null}
              </div>

              {!user ? (
                <Button asChild className="w-full">
                  <Link to={loginHref}>Sign in to approve</Link>
                </Button>
              ) : details.status === "pending" ? (
                <div className="space-y-3">
                  <p className="text-sm">
                    Signed in as <span className="font-medium">{user.username}</span>.
                  </p>
                  <Button
                    className="w-full"
                    disabled={acting}
                    onClick={() => void handleDecision("approve")}
                  >
                    {acting ? "Approving..." : "Approve sign-in"}
                  </Button>
                  <Button
                    variant="outline"
                    className="w-full"
                    disabled={acting}
                    onClick={() => void handleDecision("deny")}
                  >
                    Deny
                  </Button>
                </div>
              ) : details.status === "approved" ? (
                <p className="text-sm">Approved. Finish sign-in on the device.</p>
              ) : details.status === "consumed" ? (
                <p className="text-sm">This device is already signed in.</p>
              ) : details.status === "denied" ? (
                <p className="text-sm">This sign-in request was denied.</p>
              ) : (
                <p className="text-sm">This sign-in request has expired.</p>
              )}

              {!token ? (
                <Button
                  type="button"
                  variant="outline"
                  className="w-full"
                  onClick={() => setSearchParams({})}
                >
                  Enter another code
                </Button>
              ) : null}
            </div>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
