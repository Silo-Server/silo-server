import { LoginSessionsPanel } from "@/components/sessions/LoginSessionsPanel";
import { Button } from "@/components/ui/button";
import { useLoginSessionCapabilities } from "@/hooks/queries/loginSessions";

export default function SignedInSessions() {
  const capabilities = useLoginSessionCapabilities();
  if (capabilities.isPending) return <p role="status">Loading session settings…</p>;
  if (capabilities.isError)
    return (
      <div role="alert" className="space-y-3">
        <p>Couldn’t load session settings.</p>
        <Button onClick={() => void capabilities.refetch()}>Retry</Button>
      </div>
    );
  if (!capabilities.data?.available)
    return <p role="status">Signed-in session management is unavailable on this server.</p>;
  return <LoginSessionsPanel />;
}
