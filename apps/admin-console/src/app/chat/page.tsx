'use client';

import { useState, useRef, useCallback, useEffect } from 'react';
import { useForm } from 'react-hook-form';
import { zodResolver } from '@hookform/resolvers/zod';
import { z } from 'zod';

// ─── Form schema ──────────────────────────────────────────────────────────────

const AskFormSchema = z.object({
  prompt:         z.string().min(3).max(8192),
  dataSourceId:   z.string().uuid('Select a data source'),
});
type AskForm = z.infer<typeof AskFormSchema>;

// ─── Types ────────────────────────────────────────────────────────────────────

type NodeStatus = 'idle' | 'running' | 'done' | 'error';

interface NodeStep {
  node: string;
  status: NodeStatus;
  latency_ms?: number;
  error?: string;
}

interface ResultColumn { name: string; type?: string; masked: boolean }
interface ResultRow    { [col: string]: unknown }

interface QueryResult {
  columns:    ResultColumn[];
  rows:       ResultRow[];
  total_rows: number;
  truncated:  boolean;
  citation:   { snapshot_version: string; tables_accessed: string[]; provider?: string; model?: string };
}

interface ValidationError { code: string; message: string; hint?: string }

interface SavedQuestion {
  id: string; name: string; description?: string; prompt: string;
  data_source_id: string; run_count: number; last_run_at?: string;
}

const NODE_LABELS: Record<string, string> = {
  pep_input_sanitizer:     'Input Sanitizer',
  pep_permission_resolver: 'Permission Resolver',
  pep_retriever:           'Schema Retriever',
  pep_sql_drafter:         'SQL Drafter',
  pep_ast_validator:       'AST Validator',
  pep_cost_estimator:      'Cost Estimator',
  pep_proxy_executor:      'Proxy Executor',
  pep_result_formatter:    'Result Formatter',
};

const NODE_ORDER = Object.keys(NODE_LABELS);

// ─── Component ───────────────────────────────────────────────────────────────

export default function ChatPage() {
  const [steps, setSteps]                   = useState<NodeStep[]>([]);
  const [result, setResult]                 = useState<QueryResult | null>(null);
  const [generatedSql, setGeneratedSql]     = useState<string>('');
  const [validationErrors, setValidErrors]  = useState<ValidationError[]>([]);
  const [phase, setPhase]                   = useState<'idle' | 'running' | 'done' | 'error'>('idle');
  const [errorMsg, setErrorMsg]             = useState<string>('');
  const [sessionId, setSessionId]           = useState<string>('');
  const [savedQuestions, setSavedQuestions] = useState<SavedQuestion[]>([]);
  const [saveName, setSaveName]             = useState('');
  const [saving, setSaving]                 = useState(false);
  const [feedback, setFeedback]             = useState<'up' | 'down' | null>(null);
  const [showSql, setShowSql]               = useState(false);
  const tableRef = useRef<HTMLDivElement>(null);

  const { register, handleSubmit, formState: { errors }, setValue, watch } = useForm<AskForm>({
    resolver: zodResolver(AskFormSchema),
    defaultValues: { dataSourceId: '' },
  });

  // Load saved questions
  useEffect(() => {
    fetch('/api/ai/pep/saved-questions')
      .then((r) => r.json())
      .then((d) => setSavedQuestions(d.items ?? []))
      .catch(() => {});
  }, []);

  const onSubmit = useCallback(async (data: AskForm) => {
    setPhase('running');
    setSteps([]);
    setResult(null);
    setGeneratedSql('');
    setValidErrors([]);
    setErrorMsg('');
    setFeedback(null);

    const idempotencyKey = `chat-${Date.now()}-${Math.random().toString(36).slice(2)}`;

    try {
      const resp = await fetch('/api/ai/pep/ask', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          prompt:          data.prompt,
          data_source_id:  data.dataSourceId,
          idempotency_key: idempotencyKey,
        }),
      });

      if (!resp.ok || !resp.body) {
        const err = await resp.text();
        setErrorMsg(err || 'Request failed');
        setPhase('error');
        return;
      }

      const reader  = resp.body.getReader();
      const decoder = new TextDecoder();
      let buffer = '';

      while (true) {
        const { done, value } = await reader.read();
        if (done) break;
        buffer += decoder.decode(value, { stream: true });
        const parts = buffer.split('\n\n');
        buffer = parts.pop() ?? '';

        for (const part of parts) {
          const line = part.replace(/^data: /, '').trim();
          if (!line) continue;
          let event: Record<string, unknown>;
          try { event = JSON.parse(line); } catch { continue; }

          if (event.type === 'node') {
            setSteps((prev) => {
              const existing = prev.findIndex((s) => s.node === event.node);
              const step: NodeStep = {
                node: event.node as string,
                status: event.status as NodeStatus,
                latency_ms: event.latency_ms as number | undefined,
                error: event.error as string | undefined,
              };
              return existing >= 0
                ? prev.map((s, i) => (i === existing ? step : s))
                : [...prev, step];
            });
          }

          if (event.type === 'done') {
            setSessionId(event.session_id as string);
            if (event.validated_sql) setGeneratedSql(event.validated_sql as string);
            if (event.validation_errors) setValidErrors(event.validation_errors as ValidationError[]);
            if (event.result) setResult(event.result as QueryResult);
            if (event.error) {
              setErrorMsg(event.error as string);
              setPhase('error');
            } else {
              setPhase('done');
            }
          }
        }
      }
    } catch (err) {
      setErrorMsg(String(err));
      setPhase('error');
    }
  }, []);

  const loadSavedQuestion = (q: SavedQuestion) => {
    setValue('prompt', q.prompt);
    setValue('dataSourceId', q.data_source_id);
  };

  const submitFeedback = async (sentiment: 'up' | 'down') => {
    if (!sessionId) return;
    setFeedback(sentiment);
    await fetch('/api/ai/pep/feedback', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ session_id: sessionId, thumbs_up: sentiment === 'up' }),
    }).catch(() => {});
  };

  const saveQuestion = async () => {
    if (!sessionId || !saveName.trim()) return;
    setSaving(true);
    try {
      await fetch('/api/ai/pep/saved-questions', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ session_id: sessionId, name: saveName.trim() }),
      });
      const refreshed = await fetch('/api/ai/pep/saved-questions').then((r) => r.json());
      setSavedQuestions(refreshed.items ?? []);
      setSaveName('');
    } finally {
      setSaving(false);
    }
  };

  const phaseColors: Record<string, string> = {
    idle:    'bg-gray-100 text-gray-600',
    running: 'bg-blue-50 text-blue-700',
    done:    'bg-green-50 text-green-700',
    error:   'bg-red-50 text-red-700',
  };

  return (
    <div className="flex h-screen overflow-hidden bg-gray-50">
      {/* ── Sidebar: Saved Questions ───────────────────────────── */}
      <aside className="w-72 flex-shrink-0 border-r bg-white overflow-y-auto p-4">
        <h2 className="text-sm font-semibold text-gray-500 uppercase tracking-wider mb-3">
          Saved Questions
        </h2>
        {savedQuestions.length === 0 && (
          <p className="text-sm text-gray-400">No saved questions yet.</p>
        )}
        <ul className="space-y-2">
          {savedQuestions.map((q) => (
            <li key={q.id}>
              <button
                onClick={() => loadSavedQuestion(q)}
                className="w-full text-left px-3 py-2 rounded-lg hover:bg-gray-100 text-sm"
              >
                <div className="font-medium text-gray-800 truncate">{q.name}</div>
                <div className="text-gray-400 text-xs truncate">{q.prompt}</div>
                <div className="text-gray-400 text-xs">
                  {q.run_count} run{q.run_count !== 1 ? 's' : ''}
                  {q.last_run_at && ` · ${new Date(q.last_run_at).toLocaleDateString()}`}
                </div>
              </button>
            </li>
          ))}
        </ul>
      </aside>

      {/* ── Main Area ─────────────────────────────────────────── */}
      <main className="flex-1 flex flex-col overflow-hidden">
        {/* Header */}
        <header className="border-b bg-white px-6 py-4 flex items-center gap-3">
          <h1 className="text-lg font-semibold text-gray-900">Chat with your data</h1>
          <span className="text-xs bg-blue-100 text-blue-700 px-2 py-0.5 rounded-full font-medium">
            AI PEP Graph
          </span>
        </header>

        {/* Ask form */}
        <div className="border-b bg-white px-6 py-4">
          <form onSubmit={handleSubmit(onSubmit)} className="space-y-3">
            <div className="flex gap-3">
              <div className="flex-1">
                <textarea
                  {...register('prompt')}
                  placeholder="Ask a question about your data… e.g. 'Show me overdue invoices this quarter'"
                  rows={2}
                  className="w-full border rounded-lg px-3 py-2 text-sm resize-none focus:outline-none focus:ring-2 focus:ring-blue-500"
                />
                {errors.prompt && (
                  <p className="text-red-500 text-xs mt-1">{errors.prompt.message}</p>
                )}
              </div>
              <div className="flex flex-col gap-2">
                <select
                  {...register('dataSourceId')}
                  className="border rounded-lg px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-blue-500"
                >
                  <option value="">Select data source</option>
                  {/* Data sources loaded from API in a real app */}
                  <option value="00000000-0000-0000-0000-000000000001">orders_db</option>
                </select>
                {errors.dataSourceId && (
                  <p className="text-red-500 text-xs">{errors.dataSourceId.message}</p>
                )}
                <button
                  type="submit"
                  disabled={phase === 'running'}
                  className="bg-blue-600 hover:bg-blue-700 disabled:opacity-50 text-white rounded-lg px-4 py-2 text-sm font-medium"
                >
                  {phase === 'running' ? 'Thinking…' : 'Ask'}
                </button>
              </div>
            </div>
          </form>
        </div>

        {/* Body */}
        <div className="flex-1 overflow-y-auto px-6 py-4 space-y-4">
          {/* Progress stepper */}
          {steps.length > 0 && (
            <div className="bg-white rounded-xl border p-4">
              <h3 className="text-xs font-semibold text-gray-500 uppercase tracking-wider mb-3">Pipeline</h3>
              <div className="flex flex-wrap gap-2">
                {NODE_ORDER.map((nodeKey) => {
                  const step = steps.find((s) => s.node === nodeKey);
                  const label = NODE_LABELS[nodeKey] ?? nodeKey;
                  if (!step) {
                    return (
                      <span key={nodeKey} className="text-xs px-2 py-1 rounded-full bg-gray-100 text-gray-400">
                        {label}
                      </span>
                    );
                  }
                  return (
                    <span
                      key={nodeKey}
                      title={step.error}
                      className={`text-xs px-2 py-1 rounded-full font-medium ${
                        step.status === 'running' ? 'bg-blue-100 text-blue-700 animate-pulse' :
                        step.status === 'done'    ? 'bg-green-100 text-green-700' :
                        'bg-red-100 text-red-700'
                      }`}
                    >
                      {label}{step.latency_ms ? ` ${step.latency_ms}ms` : ''}
                    </span>
                  );
                })}
              </div>
            </div>
          )}

          {/* Validation errors */}
          {validationErrors.length > 0 && (
            <div className="bg-amber-50 border border-amber-200 rounded-xl p-4">
              <h3 className="font-medium text-amber-800 mb-2">Query adjusted</h3>
              <ul className="space-y-1">
                {validationErrors.map((e, i) => (
                  <li key={i} className="text-sm text-amber-700">
                    {e.message}{e.hint ? <span className="text-amber-500"> — {e.hint}</span> : ''}
                  </li>
                ))}
              </ul>
            </div>
          )}

          {/* Error state */}
          {phase === 'error' && errorMsg && (
            <div className="bg-red-50 border border-red-200 rounded-xl p-4">
              <p className="font-medium text-red-800">Unable to answer</p>
              <p className="text-sm text-red-700 mt-1">{errorMsg}</p>
            </div>
          )}

          {/* Generated SQL (expandable) */}
          {generatedSql && (
            <div className="bg-white rounded-xl border">
              <button
                onClick={() => setShowSql(!showSql)}
                className="w-full text-left px-4 py-3 flex items-center justify-between text-sm font-medium text-gray-700 hover:bg-gray-50 rounded-xl"
              >
                <span>Generated SQL</span>
                <span className="text-gray-400">{showSql ? '▲' : '▼'}</span>
              </button>
              {showSql && (
                <pre className="px-4 pb-4 text-xs text-gray-600 overflow-x-auto whitespace-pre-wrap font-mono">
                  {generatedSql}
                </pre>
              )}
            </div>
          )}

          {/* Result table */}
          {result && (
            <div className="bg-white rounded-xl border overflow-hidden">
              <div className="px-4 py-3 border-b flex items-center justify-between">
                <div>
                  <span className="font-medium text-gray-900">Results</span>
                  <span className="ml-2 text-sm text-gray-500">
                    {result.total_rows.toLocaleString()} row{result.total_rows !== 1 ? 's' : ''}
                    {result.truncated && ' (truncated to 10k)'}
                  </span>
                </div>
                {result.citation && (
                  <span className="text-xs text-gray-400" title={`Tables: ${result.citation.tables_accessed.join(', ')}`}>
                    {result.citation.provider ? `${result.citation.provider} · ` : ''}
                    snapshot {result.citation.snapshot_version.slice(0, 8)}
                  </span>
                )}
              </div>
              <div ref={tableRef} className="overflow-auto max-h-96">
                <table className="min-w-full text-sm">
                  <thead className="bg-gray-50 sticky top-0">
                    <tr>
                      {result.columns.map((c) => (
                        <th key={c.name} className="px-4 py-2 text-left text-xs font-semibold text-gray-500 uppercase tracking-wider whitespace-nowrap">
                          {c.name}{c.masked && <span className="ml-1 text-amber-500" title="Masked column">●</span>}
                        </th>
                      ))}
                    </tr>
                  </thead>
                  <tbody className="divide-y">
                    {result.rows.map((row, ri) => (
                      <tr key={ri} className="hover:bg-gray-50">
                        {result.columns.map((c) => (
                          <td key={c.name} className="px-4 py-2 text-gray-700 whitespace-nowrap font-mono text-xs">
                            {row[c.name] === null ? <span className="text-gray-300">NULL</span> : String(row[c.name])}
                          </td>
                        ))}
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </div>
          )}

          {/* Post-result actions */}
          {phase === 'done' && result && (
            <div className="bg-white rounded-xl border p-4 flex flex-wrap items-center gap-4">
              {/* Feedback */}
              <div className="flex items-center gap-2">
                <span className="text-sm text-gray-600">Was this helpful?</span>
                <button
                  onClick={() => submitFeedback('up')}
                  className={`text-lg ${feedback === 'up' ? 'opacity-100' : 'opacity-40 hover:opacity-70'}`}
                >👍</button>
                <button
                  onClick={() => submitFeedback('down')}
                  className={`text-lg ${feedback === 'down' ? 'opacity-100' : 'opacity-40 hover:opacity-70'}`}
                >👎</button>
              </div>

              {/* Save question */}
              <div className="flex items-center gap-2 ml-auto">
                <input
                  type="text"
                  placeholder="Save as…"
                  value={saveName}
                  onChange={(e) => setSaveName(e.target.value)}
                  className="border rounded-lg px-3 py-1.5 text-sm w-48 focus:outline-none focus:ring-2 focus:ring-blue-500"
                />
                <button
                  onClick={saveQuestion}
                  disabled={!saveName.trim() || saving}
                  className="bg-gray-800 hover:bg-gray-900 disabled:opacity-50 text-white rounded-lg px-3 py-1.5 text-sm font-medium"
                >
                  {saving ? 'Saving…' : 'Save'}
                </button>
              </div>
            </div>
          )}
        </div>
      </main>
    </div>
  );
}
