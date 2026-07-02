import { ArrowRight, PackageCheck, PenLine, ScrollText } from "lucide-react";
import { useSearchParams } from "react-router";

import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { usePolicyCapability } from "@/hooks/queries/admin/policy";
import { useDocumentTitle } from "@/hooks/useDocumentTitle";

import { PolicyDecisionLogTable } from "./PolicyDecisionLogTable";
import { PolicyDocumentList } from "./PolicyDocumentList";
import { PolicyVendorViewer } from "./PolicyVendorViewer";

// Tab ids are stable URL contract (?tab=); labels are presentation only.
const POLICY_TABS = new Set(["documents", "vendor", "decisions"]);

function resolveTab(value: string | null) {
  return value && POLICY_TABS.has(value) ? value : "documents";
}

const PIPELINE_STEPS = [
  {
    icon: PackageCheck,
    title: "Silo decides the baseline",
    detail: "Built-in rules ship with every release: profile restrictions, ratings, limits.",
  },
  {
    icon: PenLine,
    title: "Your overrides narrow it",
    detail: "Custom rules can tighten the baseline — never grant more than it allows.",
  },
  {
    icon: ScrollText,
    title: "Every decision is logged",
    detail: "Denials are always recorded; allowed requests are sampled.",
  },
] as const;

function PolicyPipelineStrip() {
  return (
    <div className="surface-panel-subtle rounded-2xl p-4">
      <ol className="grid gap-4 md:grid-cols-3">
        {PIPELINE_STEPS.map((step, index) => (
          <li key={step.title} className="relative flex items-start gap-3">
            <step.icon aria-hidden className="text-muted-foreground mt-0.5 size-4 shrink-0" />
            <div className="min-w-0">
              <p className="text-sm font-medium">{step.title}</p>
              <p className="text-muted-foreground mt-0.5 text-xs leading-relaxed">{step.detail}</p>
            </div>
            {index < PIPELINE_STEPS.length - 1 && (
              <ArrowRight
                aria-hidden
                className="text-muted-foreground/50 absolute top-1 -right-3 hidden size-4 md:block"
              />
            )}
          </li>
        ))}
      </ol>
    </div>
  );
}

export default function AdminPolicyLayout() {
  useDocumentTitle("Policy");
  const [searchParams, setSearchParams] = useSearchParams();
  const tab = resolveTab(searchParams.get("tab"));
  const capability = usePolicyCapability();

  function setTab(value: string) {
    const next = new URLSearchParams(searchParams);
    if (value === "documents") {
      next.delete("tab");
    } else {
      next.set("tab", value);
      next.delete("document");
    }
    setSearchParams(next, { replace: true });
  }

  const unavailable =
    capability.isError ||
    capability.data?.enabled === false ||
    capability.data?.editor_available === false;

  return (
    <div className="page-shell space-y-6 py-4 sm:py-6">
      <div className="page-header gap-5">
        <div className="space-y-3">
          <h1 className="page-title text-[clamp(2rem,4vw,3rem)]">Policy</h1>
          <p className="page-subtitle text-sm sm:text-base">
            Household access rules: what Silo allows by default, and where you tighten it.
          </p>
        </div>
      </div>

      {capability.isLoading && (
        <p className="text-muted-foreground text-sm">Loading policy capability...</p>
      )}

      {unavailable && (
        <div className="surface-panel-subtle rounded-2xl p-6">
          <h2 className="text-lg font-semibold">Policy workspace unavailable</h2>
          <p className="text-muted-foreground mt-2 text-sm">
            The policy engine or editor API is not available from this server process.
          </p>
        </div>
      )}

      {capability.data && !unavailable && (
        <>
          <PolicyPipelineStrip />

          <Tabs value={tab} onValueChange={setTab}>
            <TabsList>
              <TabsTrigger value="documents">Overrides</TabsTrigger>
              <TabsTrigger value="vendor">Baseline</TabsTrigger>
              <TabsTrigger value="decisions">Decision Log</TabsTrigger>
            </TabsList>

            <TabsContent value="documents" className="space-y-5">
              <PolicyDocumentList domains={capability.data.decision_types} />
            </TabsContent>
            <TabsContent value="vendor" className="space-y-5">
              <PolicyVendorViewer />
            </TabsContent>
            <TabsContent value="decisions" className="space-y-5">
              <PolicyDecisionLogTable domains={capability.data.decision_types} />
            </TabsContent>
          </Tabs>
        </>
      )}
    </div>
  );
}
