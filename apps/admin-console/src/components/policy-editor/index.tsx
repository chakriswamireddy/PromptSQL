"use client";

import dynamic from "next/dynamic";
import { useEffect, useRef, useState } from "react";
import type * as Monaco from "monaco-editor";
import { POLICY_JSON_SCHEMA, POLICY_TEMPLATE } from "@/lib/policy-schema";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { AlertCircle, CheckCircle2, Loader2 } from "lucide-react";

// Monaco must be loaded client-side only (no SSR).
const MonacoEditor = dynamic(() => import("@monaco-editor/react"), { ssr: false });

interface PolicyEditorProps {
  value?: string;
  onChange?: (value: string) => void;
  readOnly?: boolean;
  onValidate?: (valid: boolean, errors: string[]) => void;
  onSimulate?: () => void;
  onSaveDraft?: () => void;
  onSubmitForReview?: () => void;
  isSaving?: boolean;
  isSubmitting?: boolean;
  status?: string;
}

export function PolicyEditor({
  value,
  onChange,
  readOnly = false,
  onValidate,
  onSimulate,
  onSaveDraft,
  onSubmitForReview,
  isSaving,
  isSubmitting,
  status,
}: PolicyEditorProps) {
  const editorRef = useRef<Monaco.editor.IStandaloneCodeEditor | null>(null);
  const monacoRef = useRef<typeof Monaco | null>(null);
  const [parseErrors, setParseErrors] = useState<string[]>([]);
  const [isValid, setIsValid] = useState(false);

  function handleEditorMount(
    editor: Monaco.editor.IStandaloneCodeEditor,
    monaco: typeof Monaco
  ) {
    editorRef.current = editor;
    monacoRef.current = monaco;

    // Register our Policy JSON schema.
    monaco.languages.json.jsonDefaults.setDiagnosticsOptions({
      validate: true,
      schemas: [
        {
          uri: "https://platform.internal/schemas/policy-draft.json",
          fileMatch: ["*"],
          schema: POLICY_JSON_SCHEMA,
        },
      ],
    });

    // Keyboard shortcuts.
    editor.addCommand(monaco.KeyMod.CtrlCmd | monaco.KeyCode.KeyS, () => {
      onSaveDraft?.();
    });
    editor.addCommand(
      monaco.KeyMod.CtrlCmd | monaco.KeyCode.Enter,
      () => {
        onSubmitForReview?.();
      }
    );
    editor.addCommand(
      monaco.KeyMod.CtrlCmd | monaco.KeyCode.KeyK,
      () => {
        onSimulate?.();
      }
    );
  }

  function handleChange(val: string | undefined) {
    const v = val ?? "";
    onChange?.(v);
    validateJSON(v);
  }

  function validateJSON(v: string) {
    const errs: string[] = [];
    try {
      JSON.parse(v);
      setIsValid(true);
    } catch (e: unknown) {
      errs.push(e instanceof Error ? e.message : "Invalid JSON");
      setIsValid(false);
    }
    setParseErrors(errs);
    onValidate?.(errs.length === 0, errs);
  }

  // Validate initial value.
  useEffect(() => {
    if (value) validateJSON(value);
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const displayValue = value ?? POLICY_TEMPLATE;

  return (
    <div className="flex flex-col h-full border rounded-lg overflow-hidden">
      {/* Toolbar */}
      <div className="flex items-center justify-between px-3 py-2 border-b bg-muted/30 gap-2 flex-wrap">
        <div className="flex items-center gap-2">
          {isValid ? (
            <CheckCircle2 className="h-4 w-4 text-green-500" />
          ) : (
            <AlertCircle className="h-4 w-4 text-destructive" />
          )}
          <span className="text-xs text-muted-foreground">
            {parseErrors.length ? parseErrors[0] : "Valid JSON"}
          </span>
          {status && (
            <Badge variant="outline" className="text-xs">
              {status.replace("_", " ")}
            </Badge>
          )}
        </div>

        {!readOnly && (
          <div className="flex items-center gap-2">
            <span className="text-xs text-muted-foreground hidden sm:block">
              ⌘S save · ⌘↵ submit · ⌘K simulate
            </span>
            {onSimulate && (
              <Button size="sm" variant="outline" onClick={onSimulate}>
                Simulate
              </Button>
            )}
            {onSaveDraft && (
              <Button
                size="sm"
                variant="outline"
                onClick={onSaveDraft}
                disabled={isSaving || !isValid}
              >
                {isSaving && <Loader2 className="h-3 w-3 mr-1 animate-spin" />}
                Save draft
              </Button>
            )}
            {onSubmitForReview && status === "draft" && (
              <Button
                size="sm"
                onClick={onSubmitForReview}
                disabled={isSubmitting || !isValid}
              >
                {isSubmitting && <Loader2 className="h-3 w-3 mr-1 animate-spin" />}
                Submit for review
              </Button>
            )}
          </div>
        )}
      </div>

      {/* Editor */}
      <div className="flex-1 min-h-0">
        <MonacoEditor
          height="100%"
          defaultLanguage="json"
          value={displayValue}
          onChange={handleChange}
          onMount={handleEditorMount}
          options={{
            readOnly,
            minimap: { enabled: false },
            scrollBeyondLastLine: false,
            fontSize: 13,
            lineNumbers: "on",
            wordWrap: "on",
            formatOnPaste: true,
            formatOnType: false,
            tabSize: 2,
            automaticLayout: true,
            accessibilitySupport: "on",
          }}
        />
      </div>
    </div>
  );
}
