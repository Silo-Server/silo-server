import { useState } from "react";
import type { FormEvent } from "react";
import { useNavigate } from "react-router";
import { useCreateNode } from "@/hooks/queries/admin/nodes";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Check, ChevronRight, Plus } from "lucide-react";
import { useWizardContext } from "../WizardContext";

interface AddedNode {
  name: string;
  type: string;
}

function AddNodeForm({ onAdded }: { onAdded: (node: AddedNode) => void }) {
  const createNode = useCreateNode();
  const [name, setName] = useState("");
  const [url, setUrl] = useState("");
  const [type, setType] = useState<"proxy" | "transcode">("proxy");

  async function handleSubmit(e: FormEvent) {
    e.preventDefault();
    if (!name.trim() || !url.trim()) return;
    try {
      await createNode.mutateAsync({ name: name.trim(), type, url: url.trim() });
      onAdded({ name: name.trim(), type });
      setName("");
      setUrl("");
    } catch {
      // Error toast handled by mutation hook
    }
  }

  return (
    <form
      onSubmit={handleSubmit}
      className="border-foreground/[0.07] bg-foreground/[0.03] animate-[fade-in_0.15s_ease-out] space-y-3 rounded-xl border p-4"
    >
      <div className="grid gap-3 sm:grid-cols-3">
        <div className="space-y-1">
          <Label htmlFor="node-name" className="text-xs">
            Name
          </Label>
          <Input
            id="node-name"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="proxy-us-east"
            className="h-8 text-sm"
            required
          />
        </div>
        <div className="space-y-1">
          <Label htmlFor="node-type" className="text-xs">
            Type
          </Label>
          <Select value={type} onValueChange={(v) => setType(v as "proxy" | "transcode")}>
            <SelectTrigger id="node-type" className="h-8 text-sm">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="proxy">Proxy</SelectItem>
              <SelectItem value="transcode">Transcode</SelectItem>
            </SelectContent>
          </Select>
        </div>
        <div className="space-y-1">
          <Label htmlFor="node-url" className="text-xs">
            URL
          </Label>
          <Input
            id="node-url"
            value={url}
            onChange={(e) => setUrl(e.target.value)}
            placeholder="https://proxy.example.com"
            className="h-8 text-sm"
            required
          />
        </div>
      </div>
      <Button
        type="submit"
        variant="secondary"
        size="sm"
        className="h-7 text-xs"
        disabled={createNode.isPending}
      >
        {createNode.isPending ? "Adding..." : "Add node"}
      </Button>
    </form>
  );
}

export function NodesFinishStep() {
  const navigate = useNavigate();
  const { clearProgress, profile, profiles, selectProfile } = useWizardContext();
  const [finishing, setFinishing] = useState(false);
  const [addedNodes, setAddedNodes] = useState<AddedNode[]>([]);
  const [showNodeForm, setShowNodeForm] = useState(false);

  function handleNodeAdded(node: AddedNode) {
    setAddedNodes((prev) => [...prev, node]);
  }

  function completeSetup(destination: string) {
    const chosenProfile = profile ?? profiles[0] ?? null;
    if (chosenProfile && !profile) {
      selectProfile(chosenProfile);
    }
    clearProgress();
    navigate(destination);
  }

  function handleFinish() {
    setFinishing(true);
    completeSetup("/");
  }

  function handleGoToAdmin() {
    setFinishing(true);
    completeSetup("/admin/libraries");
  }

  return (
    <div className="space-y-5">
      {/* Worker nodes section */}
      {addedNodes.length > 0 && (
        <div className="border-foreground/[0.07] bg-foreground/[0.03] space-y-1.5 rounded-xl border p-4">
          {addedNodes.map((node, i) => (
            <div key={node.name + i} className="flex items-center gap-2 text-sm">
              <Check className="h-3.5 w-3.5 text-green-500" />
              <span className="font-medium">{node.name}</span>
              <span className="bg-foreground/8 text-muted-foreground rounded px-1.5 py-0.5 text-[10px]">
                {node.type}
              </span>
            </div>
          ))}
        </div>
      )}

      {showNodeForm ? (
        <AddNodeForm onAdded={handleNodeAdded} />
      ) : (
        <button
          type="button"
          onClick={() => setShowNodeForm(true)}
          className="text-muted-foreground hover:text-foreground flex items-center gap-1.5 text-sm transition-colors"
        >
          <Plus className="h-3.5 w-3.5" />
          Add a worker node
        </button>
      )}

      {/* Finish CTAs */}
      <div className="border-foreground/[0.06] flex flex-col gap-3 border-t pt-6 sm:flex-row">
        <Button onClick={handleFinish} disabled={finishing} className="sm:flex-1">
          {finishing ? "Starting..." : "Start using Silo"}
          <ChevronRight className="ml-1.5 h-4 w-4" />
        </Button>
        <Button
          type="button"
          variant="secondary"
          onClick={handleGoToAdmin}
          disabled={finishing}
          className="sm:flex-1"
        >
          Go to admin
        </Button>
      </div>
    </div>
  );
}
