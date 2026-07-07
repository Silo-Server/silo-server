import { linter } from "@codemirror/lint";
import { EditorState } from "@codemirror/state";
import { EditorView, lineNumbers } from "@codemirror/view";
import CodeMirror from "@uiw/react-codemirror";
import { useMemo } from "react";

import type { PolicyCompileIssue } from "@/api/types";
import { mapPolicyIssuesToDiagnostics } from "@/lib/policyDiagnostics";
import { regoLanguage } from "@/lib/regoLanguage";
import { cn } from "@/lib/utils";

const editorTheme = EditorView.theme({
  "&": {
    fontSize: "13px",
  },
  ".cm-content": {
    fontFamily:
      'ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", monospace',
  },
  ".cm-gutters": {
    fontFamily:
      'ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", monospace',
  },
});

export interface RegoEditorProps {
  value: string;
  onChange?: (value: string) => void;
  issues?: readonly PolicyCompileIssue[];
  readOnly?: boolean;
  height?: string;
  ariaLabel?: string;
  className?: string;
}

export function RegoEditor({
  value,
  onChange,
  issues = [],
  readOnly = false,
  height = "360px",
  ariaLabel = "Rego policy source",
  className,
}: RegoEditorProps) {
  const extensions = useMemo(() => {
    const diagnostics = mapPolicyIssuesToDiagnostics(value, issues);
    return [
      lineNumbers(),
      regoLanguage,
      EditorView.lineWrapping,
      EditorState.readOnly.of(readOnly),
      EditorView.editable.of(!readOnly),
      linter(() => diagnostics),
      editorTheme,
    ];
  }, [issues, readOnly, value]);

  return (
    <div className={cn("border-border overflow-hidden rounded-lg border", className)}>
      <CodeMirror
        value={value}
        height={height}
        basicSetup={false}
        extensions={extensions}
        onChange={(nextValue) => onChange?.(nextValue)}
        aria-label={ariaLabel}
      />
    </div>
  );
}
