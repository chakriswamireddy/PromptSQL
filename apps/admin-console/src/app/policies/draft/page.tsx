'use client';

import { useState, useRef, useCallback } from 'react';
import { useForm } from 'react-hook-form';
import { zodResolver } from '@hookform/resolvers/zod';
import { z } from 'zod';
import dynamic from 'next/dynamic';

const MonacoEditor = dynamic(() => import('@monaco-editor/react'), { ssr: false });

const DraftFormSchema = z.object({
  prompt: z.string().min(10).max(4096),
});
type DraftForm = z.infer<typeof DraftFormSchema>;

type NodeStatus = 'idle' | 'running' | 'done' | 'error';

interface NodeStep {
  node: string;
  status: NodeStatus;
  latency_ms?: number;
  error?: string;
}

const NODE_LABELS: Record<string, string> = {
  input_sanitizer: 'Input Sanitizer',
  intent_parser:   'Intent Parser',
  schema_resolver: 'Schema Resolver',
  policy_drafter:  'Policy Drafter',
  policy_validator:'Policy Validator',
  simulator:       'Simulator',
  audit_explainer: 'Audit Explainer',
  human_approval:  'Human Approval',
};

export default function AiDraftPage() {
  const [steps, setSteps] = useState<NodeStep[]>([]);
  const [draftPolicy, setDraftPolicy] = useState<unknown>(null);
  const [explanation, setExplanation] = useState<string>('');
  const [simulatorDiff, setSimulatorDiff] = useState<unknown>(null);
  const [validationErrors, setValidationErrors] = useState<string[]>([]);
  const [sessionId, setSessionId] = useState<string | null>(null);
  const [totalCost, setTotalCost] = useState<number>(0);
  const [phase, setPhase] = useState<'idle' | 'drafting' | 'review' | 'approving' | 'done'>('idle');
  const [error, setError] = useState<string | null>(null);
  const idempotencyKey = useRef(crypto.randomUUID());

  const { register, handleSubmit, formState: { errors } } = useForm<DraftForm>({
    resolver: zodResolver(DraftFormSchema),
  });

  const updateStep = useCallback((node: string, update: Partial<NodeStep>) => {
    setSteps((prev) => {
      const idx = prev.findIndex((s) => s.node === node);
      if (idx === -1) return [...prev, { node, status: 'idle', ...update }];
      const next = [...prev];
      next[idx] = { ...next[idx]!, ...update };
      return next;
    });
  }, []);

  const onSubmit = useCallback(async (data: DraftForm) => {
    setPhase('drafting');
    setError(null);
    setSteps([]);
    setDraftPolicy(null);
    setExplanation('');
    setSimulatorDiff(null);
    setValidationErrors([]);
    idempotencyKey.current = crypto.randomUUID();

    try {
      const resp = await fetch('/api/ai/pap/draft', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ prompt: data.prompt, idempotency_key: idempotencyKey.current }),
      });

      if (!resp.ok) {
        const err = await resp.json();
        setError(err.message ?? 'Draft failed');
        setPhase('idle');
        return;
      }

      const contentType = resp.headers.get('content-type') ?? '';
      if (!contentType.includes('event-stream')) {
        const json = await resp.json();
        if (json.draft_policy) setDraftPolicy(json.draft_policy);
        if (json.session_id) setSessionId(json.session_id);
        setPhase('review');
        return;
      }

      const reader = resp.body!.getReader();
      const decoder = new TextDecoder();
      let buf = '';

      while (true) {
        const { done, value } = await reader.read();
        if (done) break;
        buf += decoder.decode(value, { stream: true });
        const lines = buf.split('\n');
        buf = lines.pop() ?? '';

        for (const line of lines) {
          if (!line.startsWith('data: ')) continue;
          const event = JSON.parse(line.slice(6)) as Record<string, unknown>;

          if (event.type === 'node') {
            updateStep(
              event.node as string,
              { status: event.status as NodeStatus, latency_ms: event.latency_ms as number, error: event.error as string },
            );
          } else if (event.type === 'done') {
            setSessionId(event.session_id as string);
            if (event.draft_policy) setDraftPolicy(event.draft_policy);
            if (event.explanation) setExplanation(event.explanation as string);
            if (event.simulator_diff) setSimulatorDiff(event.simulator_diff);
            if (event.validation_errors) setValidationErrors(event.validation_errors as string[]);
            if (event.total_cost_usd) setTotalCost(event.total_cost_usd as number);
            if (event.error) {
              setError(event.error as string);
              setPhase('idle');
            } else {
              setPhase('review');
            }
          }
        }
      }
    } catch (e) {
      setError(String(e));
      setPhase('idle');
    }
  }, [updateStep]);

  const handleApprove = useCallback(async (action: 'approve' | 'reject') => {
    if (!sessionId) return;
    const mfaToken = prompt('Enter your MFA token to ' + action + ':');
    if (!mfaToken) return;
    setPhase('approving');
    try {
      const resp = await fetch('/api/ai/pap/approve', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ session_id: sessionId, action, mfa_token: mfaToken }),
      });
      if (!resp.ok) {
        const err = await resp.json();
        setError(err.message ?? 'Approval failed');
        setPhase('review');
        return;
      }
      setPhase('done');
    } catch (e) {
      setError(String(e));
      setPhase('review');
    }
  }, [sessionId]);

  return (
    <div className="max-w-5xl mx-auto p-6 space-y-6">
      <h1 className="text-2xl font-bold">AI Policy Drafter</h1>
      <p className="text-muted-foreground text-sm">
        Describe an access-control policy in plain English. The AI will draft it, run it
        through the simulator, and present it for your approval. You approve the <strong>JSON</strong>, not the description.
      </p>

      {/* Prompt form */}
      <form onSubmit={handleSubmit(onSubmit)} className="space-y-3">
        <textarea
          {...register('prompt')}
          className="w-full border rounded p-3 font-mono text-sm h-28 resize-none"
          placeholder='e.g. "Give finance managers read-only access to payments for their own campus, hide bank account numbers"'
          disabled={phase !== 'idle' && phase !== 'done'}
        />
        {errors.prompt && <p className="text-red-500 text-xs">{errors.prompt.message}</p>}
        <button
          type="submit"
          disabled={phase !== 'idle' && phase !== 'done'}
          className="px-4 py-2 bg-blue-600 text-white rounded disabled:opacity-50"
        >
          {phase === 'drafting' ? 'Drafting…' : 'Draft Policy'}
        </button>
      </form>

      {error && (
        <div className="border border-red-300 bg-red-50 rounded p-3 text-red-700 text-sm">
          {error}
        </div>
      )}

      {/* Graph stepper */}
      {steps.length > 0 && (
        <div className="border rounded p-4 space-y-2">
          <h2 className="font-semibold text-sm text-muted-foreground uppercase tracking-wide">Graph Progress</h2>
          {steps.map((s) => (
            <div key={s.node} className="flex items-center gap-3 text-sm">
              <span className={
                s.status === 'done'    ? 'text-green-600' :
                s.status === 'running' ? 'text-blue-500 animate-pulse' :
                s.status === 'error'   ? 'text-red-500' : 'text-gray-400'
              }>
                {s.status === 'done' ? '✓' : s.status === 'error' ? '✗' : s.status === 'running' ? '●' : '○'}
              </span>
              <span className="font-medium">{NODE_LABELS[s.node] ?? s.node}</span>
              {s.latency_ms != null && <span className="text-muted-foreground ml-auto">{s.latency_ms}ms</span>}
              {s.error && <span className="text-red-500 ml-2 truncate">{s.error}</span>}
            </div>
          ))}
          {totalCost > 0 && (
            <p className="text-xs text-muted-foreground pt-2 border-t">
              Estimated cost: ${totalCost.toFixed(4)} USD
            </p>
          )}
        </div>
      )}

      {/* Review pane */}
      {(phase === 'review' || phase === 'approving' || phase === 'done') && draftPolicy && (
        <div className="grid grid-cols-2 gap-4">
          {/* JSON editor — admin approves THE JSON */}
          <div className="border rounded overflow-hidden">
            <div className="px-3 py-2 bg-muted text-xs font-semibold uppercase tracking-wide border-b">
              Generated Policy JSON (editable)
            </div>
            <MonacoEditor
              height="400px"
              defaultLanguage="json"
              value={JSON.stringify(draftPolicy, null, 2)}
              options={{ minimap: { enabled: false }, fontSize: 12, readOnly: phase === 'done' }}
            />
          </div>

          {/* Simulator diff + explanation */}
          <div className="space-y-3">
            {validationErrors.length > 0 && (
              <div className="border border-yellow-300 bg-yellow-50 rounded p-3 text-yellow-800 text-xs space-y-1">
                <p className="font-semibold">Validation warnings</p>
                {validationErrors.map((e, i) => <p key={i}>• {e}</p>)}
              </div>
            )}
            {explanation && (
              <div className="border rounded p-3 text-sm space-y-2">
                <p className="font-semibold text-xs uppercase tracking-wide text-muted-foreground">Plain-English Explanation</p>
                <p className="whitespace-pre-line">{explanation}</p>
              </div>
            )}
            {simulatorDiff && (
              <div className="border rounded p-3 text-xs font-mono overflow-auto max-h-48">
                <p className="font-semibold mb-2">Simulator Preview</p>
                <pre>{JSON.stringify(simulatorDiff, null, 2)}</pre>
              </div>
            )}
          </div>
        </div>
      )}

      {/* Approval buttons */}
      {phase === 'review' && sessionId && (
        <div className="flex gap-3 pt-2">
          <button
            onClick={() => handleApprove('approve')}
            className="px-6 py-2 bg-green-600 text-white rounded font-semibold"
          >
            Approve Policy
          </button>
          <button
            onClick={() => handleApprove('reject')}
            className="px-6 py-2 border border-red-400 text-red-600 rounded font-semibold"
          >
            Reject
          </button>
          <p className="text-xs text-muted-foreground self-center">
            MFA required. You are approving the <strong>JSON above</strong>, not the description.
          </p>
        </div>
      )}

      {phase === 'done' && (
        <div className="border border-green-300 bg-green-50 rounded p-3 text-green-800 text-sm font-semibold">
          Policy submitted for dual-approval review. A second approver must activate it.
        </div>
      )}
    </div>
  );
}
