import type { PepGraphState } from '../../pep-schemas';
import type { Config } from '../../../config';

// Result Formatter: applies output-side masks and attaches citation metadata.
// The proxy already applied column masks; this is a safety re-check at the
// output edge, plus formatting for the chat UI.

// Mask functions (mirror what the proxy applies)
const MASK_FUNCTIONS: Record<string, (value: unknown) => unknown> = {
  redact:    () => '***',
  partial:   (v) => {
    const s = String(v ?? '');
    if (s.length <= 4) return '***';
    return s.slice(0, 2) + '***' + s.slice(-2);
  },
  hash:      (v) => `[hashed:${String(v ?? '').slice(0, 4)}...]`,
  tokenize:  (v) => `[token:${String(v ?? '').slice(0, 4)}...]`,
};

export function pepResultFormatterNode(
  state: PepGraphState,
  _cfg: Config,
): Partial<PepGraphState> {
  const start = Date.now();

  if (!state.result) {
    return span(state, start, { error: 'No result to format', abort_reason: 'formatter_no_result' });
  }

  // Build masked-column lookup from snapshot
  const maskedColumns = new Map<string, string>();
  if (state.allowed_snapshot) {
    for (const table of state.allowed_snapshot.tables) {
      for (const col of table.columns) {
        if (col.masked) maskedColumns.set(col.name, col.masked);
      }
    }
  }

  // Apply masks to rows (safety re-check; proxy should have done this already)
  const maskedRows = state.result.rows.map((row) => {
    const out: Record<string, unknown> = {};
    for (const [k, v] of Object.entries(row)) {
      const maskType = maskedColumns.get(k);
      out[k] = maskType ? (MASK_FUNCTIONS[maskType]?.(v) ?? v) : v;
    }
    return out;
  });

  // Update column metadata with mask status
  const columns = state.result.columns.map((c) => ({
    ...c,
    masked: maskedColumns.has(c.name),
  }));

  // Attach LLM provider info to citation
  const lastDrafterSpan = [...(state.node_spans ?? [])]
    .reverse()
    .find((s) => s.node === 'pep_sql_drafter');

  const result = {
    ...state.result,
    rows: maskedRows,
    columns,
    citation: {
      ...state.result.citation,
      provider: lastDrafterSpan?.provider,
      model: lastDrafterSpan?.model,
    },
  };

  return span(state, start, { result });
}

function span(state: PepGraphState, start: number, patch: Partial<PepGraphState>): Partial<PepGraphState> {
  return {
    ...patch,
    node_spans: [
      ...(state.node_spans ?? []),
      { node: 'pep_result_formatter', latency_ms: Date.now() - start, error: patch.error },
    ],
  };
}
