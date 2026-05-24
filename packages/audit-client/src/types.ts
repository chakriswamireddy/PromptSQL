export const SCHEMA_V1 = "v1" as const;

export type PolicyAction =
  | "policy.create"
  | "policy.activate"
  | "policy.archive"
  | "policy.simulate"
  | "policy.update"
  | "policy.review";

export type AccessDecision = "allow" | "deny" | "error";

export interface EventMeta {
  requestId?: string;
  traceId?: string;
  ip?: string;
  userAgent?: string;
  mfaAt?: number;
}

export interface PolicyEventInput {
  action: PolicyAction;
  policyId: string;
  beforeState?: unknown;
  afterState?: unknown;
  metadata?: EventMeta;
}

export interface AccessEventInput {
  userId: string;
  tenantId: string;
  dataSourceId: string;
  resource: string;
  action: string;
  decision: AccessDecision;
  reason?: string;
  rowCount?: number;
  queryHash?: string;
  durationMs: number;
  riskScore?: number;
  breakGlass?: boolean;
  policyVersion?: string;
  metadata?: EventMeta;
}

export interface SystemEventInput {
  tenantId?: string;
  action: string;
  detail?: unknown;
  metadata?: EventMeta;
}

// Fully-formed wire payloads (after enrichment with eventId, schema, etc.)
export interface PolicyEventPayload extends PolicyEventInput {
  eventId: string;
  schema: typeof SCHEMA_V1;
  service: string;
  eventTime: string;
  tenantId: string;
  actorId: string;
  actorToken?: string;
}

export interface AccessEventPayload extends AccessEventInput {
  eventId: string;
  schema: typeof SCHEMA_V1;
  service: string;
  eventTime: string;
  actorToken?: string;
}

export interface SystemEventPayload extends SystemEventInput {
  eventId: string;
  schema: typeof SCHEMA_V1;
  service: string;
  eventTime: string;
}
