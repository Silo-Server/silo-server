import { CircleCheck, CircleAlert } from "lucide-react";

export function CredentialStatus({ configured }: { configured: boolean }) {
  if (configured) {
    return (
      <span className="text-muted-foreground inline-flex items-center gap-1 text-xs">
        <CircleCheck className="h-3.5 w-3.5 text-green-500" />
        Configured
      </span>
    );
  }
  return (
    <span className="text-muted-foreground inline-flex items-center gap-1 text-xs">
      <CircleAlert className="h-3.5 w-3.5 text-yellow-500" />
      Not configured
    </span>
  );
}
