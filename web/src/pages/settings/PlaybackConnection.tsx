import { useQuery } from "@tanstack/react-query";
import { v2 } from "@/api/v2/request";
import { SettingRow } from "@/components/settings/SettingRow";
import { Button } from "@/components/ui/button";
import { toast } from "sonner";

export function PlaybackConnection() {
  const capabilities = useQuery({
    queryKey: ["playback", "connection-capabilities"],
    queryFn: () => v2("GET /api/v2/playback/capabilities"),
  });
  const connection = useQuery({
    queryKey: ["playback", "connection"],
    queryFn: () => v2("GET /api/v2/playback/connection"),
    enabled: capabilities.data?.features.includes("buffering_quality_v1") === true,
  });
  const limit = connection.data?.learned_limit;
  if (!limit) return null;
  return (
    <SettingRow
      label="Learned limit"
      description={`Limited to ${(limit.kbps / 1000).toLocaleString(undefined, { maximumFractionDigits: 1 })} Mbps after buffering on ${new Date(limit.observed_at).toLocaleDateString()}`}
      control={() => (
        <Button
          variant="outline"
          onClick={() => {
            void v2("DELETE /api/v2/playback/connection/learned-limit")
              .then(() => connection.refetch())
              .catch(() => toast.error("Failed to reset learned limit"));
          }}
        >
          Reset
        </Button>
      )}
    />
  );
}
