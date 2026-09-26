import { useMemo } from "react";

import { parsePluginMarkdown, type PluginMarkdownInline } from "@/lib/pluginMarkdown";
import { safeExternalURL } from "@/lib/pluginPresentation";
import { cn } from "@/lib/utils";

function Inline({ node }: { node: PluginMarkdownInline }) {
  switch (node.kind) {
    case "strong":
      return <strong className="text-foreground font-semibold">{node.text}</strong>;
    case "em":
      return <em>{node.text}</em>;
    case "code":
      return <code className="bg-muted rounded px-1 py-0.5 font-mono text-xs">{node.text}</code>;
    case "link": {
      const href = safeExternalURL(node.href);
      if (!href) return <>{node.text}</>;
      return (
        <a
          href={href}
          target="_blank"
          rel="noopener noreferrer"
          className="text-foreground underline underline-offset-4"
        >
          {node.text}
        </a>
      );
    }
    default:
      return <>{node.text}</>;
  }
}

function Inlines({ nodes }: { nodes: PluginMarkdownInline[] }) {
  return nodes.map((node, index) => <Inline key={index} node={node} />);
}

/** Renders plugin-supplied Markdown as plain React text; see parsePluginMarkdown. */
export function PluginMarkdown({ source, className }: { source?: string; className?: string }) {
  const blocks = useMemo(() => parsePluginMarkdown(source), [source]);
  if (blocks.length === 0) return null;
  return (
    <div className={cn("space-y-2.5 text-sm leading-relaxed", className)}>
      {blocks.map((block, index) => {
        if (block.kind === "paragraph") {
          return (
            <p key={index}>
              <Inlines nodes={block.inlines} />
            </p>
          );
        }
        const List = block.ordered ? "ol" : "ul";
        return (
          <List
            key={index}
            className={cn("space-y-1 pl-5", block.ordered ? "list-decimal" : "list-disc")}
          >
            {block.items.map((item, itemIndex) => (
              <li key={itemIndex}>
                <Inlines nodes={item} />
              </li>
            ))}
          </List>
        );
      })}
    </div>
  );
}
