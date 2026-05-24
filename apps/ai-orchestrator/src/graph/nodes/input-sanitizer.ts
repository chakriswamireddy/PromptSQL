import crypto from 'crypto';
import type { GraphState } from '../schemas';
import { injectionRefusalsTotal } from '../../metrics';

// Control-sequence and prompt-injection patterns
const INJECTION_PATTERNS: RegExp[] = [
  /ignore\s+(previous|prior|above|all)\s+instructions?/i,
  /you\s+are\s+now\s+a?n?\s*\w*\s*(assistant|bot|AI)/i,
  /system\s*[:=]\s*(prompt|message|instruction)/i,
  /\[INST\]|\[\/INST\]|<\|im_start\|>|<\|im_end\|>/,
  /----\s*(system|user|assistant)\s*----/i,
  /\x00|\x01|\x02|\x03|\x04|\x05|\x06|\x07|\x08|\x0b|\x0c|\x0e|\x0f/,
];

const MAX_PROMPT_BYTES = 4096;

export function inputSanitizerNode(state: GraphState): Partial<GraphState> {
  const start = Date.now();

  // Length guard
  const byteLen = Buffer.byteLength(state.prompt, 'utf8');
  if (byteLen > MAX_PROMPT_BYTES) {
    return appendSpan(state, 'input_sanitizer', Date.now() - start, {
      error: `Prompt exceeds ${MAX_PROMPT_BYTES} bytes (${byteLen})`,
      abort_reason: 'prompt_too_long',
    });
  }

  // Injection detection
  for (const pattern of INJECTION_PATTERNS) {
    if (pattern.test(state.prompt)) {
      injectionRefusalsTotal.inc({ tenant_id: state.tenant_id });
      return appendSpan(state, 'input_sanitizer', Date.now() - start, {
        error: 'Prompt injection attempt detected',
        abort_reason: 'injection_detected',
      });
    }
  }

  // Strip null bytes and dangerous control characters, trim
  const sanitized = state.prompt
    .replace(/[\x00-\x08\x0b\x0c\x0e-\x1f\x7f]/g, '')
    .trim();

  const promptHash = crypto.createHash('sha256').update(sanitized).digest('hex');

  return appendSpan(state, 'input_sanitizer', Date.now() - start, {
    sanitized_prompt: sanitized,
    prompt_hash: promptHash,
  });
}

function appendSpan(
  state: GraphState,
  node: string,
  latency_ms: number,
  patch: Partial<GraphState>,
): Partial<GraphState> {
  return {
    ...patch,
    node_spans: [
      ...(state.node_spans ?? []),
      { node, latency_ms, error: patch.error },
    ],
  };
}
