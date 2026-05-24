import crypto from 'crypto';
import type { PepGraphState } from '../../pep-schemas';
import { injectionRefusalsTotal } from '../../../metrics';

// SQL injection patterns in natural language — prompts that embed raw SQL
// are rejected entirely; the drafter generates SQL from scratch.
const INJECTION_PATTERNS: RegExp[] = [
  /ignore\s+(previous|prior|above|all)\s+instructions?/i,
  /you\s+are\s+now\s+a?n?\s*\w*\s*(assistant|bot|AI)/i,
  /system\s*[:=]\s*(prompt|message|instruction)/i,
  /\[INST\]|\[\/INST\]|<\|im_start\|>|<\|im_end\|>/,
  /----\s*(system|user|assistant)\s*----/i,
  // Raw SQL injection attempts embedded in NL
  /;\s*(drop|truncate|delete|update|insert|alter|create|grant|revoke)\s/i,
  /union\s+(all\s+)?select\s+/i,
  /--\s*$|\/\*.*?\*\//ms,
  /\x00|\x01|\x02|\x03|\x04|\x05|\x06|\x07|\x08|\x0b|\x0c|\x0e|\x0f/,
];

// Phrases that sound like attempts to exfiltrate schema or bypass row-level security
const EXFILTRATION_PATTERNS: RegExp[] = [
  /show\s+(me\s+)?(all\s+)?(tables|columns|schemas|databases|users)/i,
  /information_schema|pg_catalog|pg_class|pg_user/i,
  /bypass\s+(rls|row.?level|policy|security)/i,
  /as\s+(super)?admin|as\s+root|with\s+privileges/i,
];

const MAX_PROMPT_BYTES = 8192;

export function pepInputSanitizerNode(state: PepGraphState): Partial<PepGraphState> {
  const start = Date.now();

  const byteLen = Buffer.byteLength(state.prompt, 'utf8');
  if (byteLen > MAX_PROMPT_BYTES) {
    return span(state, start, {
      error: `Prompt exceeds ${MAX_PROMPT_BYTES} bytes`,
      abort_reason: 'prompt_too_long',
    });
  }

  for (const pattern of INJECTION_PATTERNS) {
    if (pattern.test(state.prompt)) {
      injectionRefusalsTotal.inc({ tenant_id: state.tenant_id });
      return span(state, start, {
        error: 'Prompt injection attempt detected',
        abort_reason: 'injection_detected',
      });
    }
  }

  for (const pattern of EXFILTRATION_PATTERNS) {
    if (pattern.test(state.prompt)) {
      injectionRefusalsTotal.inc({ tenant_id: state.tenant_id });
      return span(state, start, {
        error: 'Potential schema exfiltration attempt detected',
        abort_reason: 'exfiltration_attempt',
      });
    }
  }

  const sanitized = state.prompt
    .replace(/[\x00-\x08\x0b\x0c\x0e-\x1f\x7f]/g, '')
    .trim();

  const promptHash = crypto.createHash('sha256').update(sanitized).digest('hex');

  return span(state, start, {
    sanitized_prompt: sanitized,
    prompt_hash: promptHash,
  });
}

function span(state: PepGraphState, start: number, patch: Partial<PepGraphState>): Partial<PepGraphState> {
  return {
    ...patch,
    node_spans: [
      ...(state.node_spans ?? []),
      { node: 'pep_input_sanitizer', latency_ms: Date.now() - start, error: patch.error },
    ],
  };
}
