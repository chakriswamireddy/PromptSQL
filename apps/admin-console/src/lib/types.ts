/**
 * Canonical API types shared across the admin console.
 * These mirror the Zod schemas in packages/shared-types and the Go structs.
 */

export interface TokenResponse {
  accessToken: string;
  refreshToken: string;
  expiresIn: number;
  tokenType: "Bearer";
}

export type PolicyStatus = "draft" | "pending_review" | "active" | "archived";
export type PolicyEffect = "allow" | "deny";

export interface Policy {
  id: string;
  tenantId: string;
  name: string;
  version: number;
  status: PolicyStatus;
  effect: PolicyEffect;
  subjectMatch: Record<string, unknown>;
  resourceMatch: Record<string, unknown>;
  action: string;
  conditions?: Record<string, unknown>;
  obligations?: Record<string, unknown> | unknown[];
  allowedColumns?: string[];
  deniedColumns?: string[];
  rowFilter?: Record<string, unknown>;
  columnMasks?: Record<string, string>;
  createdBy: string;
  approvedBy?: string;
  submittedBy?: string;
  submittedAt?: string;
  effectiveFrom?: string;
  effectiveTo?: string;
  createdAt: string;
  updatedAt: string;
  etag?: string;
}

export interface PolicyListResponse {
  items: Policy[];
  nextCursor?: string;
  total: number;
}

export interface User {
  id: string;
  tenantId: string;
  email: string;
  name: string;
  status: "active" | "suspended" | "deprovisioned";
  roles: string[];
  mfaEnabled: boolean;
  lastLoginAt?: string;
  createdAt: string;
}

export interface UserListResponse {
  items: User[];
  nextCursor?: string;
  total: number;
}

export interface Role {
  id: string;
  tenantId: string;
  name: string;
  description?: string;
  parentRoleId?: string;
  permissions: string[];
  createdAt: string;
}

export interface RoleListResponse {
  items: Role[];
}

export interface DataSource {
  id: string;
  tenantId: string;
  name: string;
  kind: string;
  host: string;
  port: number;
  database: string;
  status: "connected" | "disconnected" | "error";
  createdAt: string;
}

export interface DataSourceListResponse {
  items: DataSource[];
}

export interface Persona {
  id: string;
  tenantId: string;
  name: string;
  description?: string;
  attributes: Record<string, unknown>;
  ownerUserId: string;
  createdAt: string;
}

export interface PersonaListResponse {
  items: Persona[];
}

export interface SimulateRequest {
  policyIds?: string[];
  useDraft?: string;
  subject: {
    userId?: string;
    personaId?: string;
    attributes?: Record<string, unknown>;
  };
  action: string;
  resource: string;
  dataSourceId: string;
  context?: Record<string, string>;
}

export interface SimulateResult {
  effect: "PERMIT" | "DENY";
  reason: string;
  matchedPolicies: Array<{ id: string; name: string; effect: string }>;
  allowedColumns: string[];
  deniedColumns: string[];
  columnMasks: Record<string, string>;
  rowFilter?: Record<string, unknown>;
  obligations: Array<{ kind: string; parameters: Record<string, unknown> }>;
  ttlSeconds: number;
}

export interface DiffRequest {
  draftPolicyId: string;
  sampleSize?: number;
}

export interface DiffReport {
  id: string;
  draftHash: string;
  activeHash: string;
  sampleSize: number;
  createdAt: string;
  summary: {
    newlyPermittedColumns: string[];
    newlyDeniedColumns: string[];
    newlyBlockedRows: number;
    newlyPermittedRows: number;
    newObligations: string[];
    affectedUsersEstimate: number;
    severity: "none" | "low" | "medium" | "high" | "critical";
  };
  perRoleDiff: Array<{
    role: string;
    sampleCount: number;
    permitTodeny: number;
    denyToPermit: number;
    columnChanges: string[];
  }>;
  topAffectedUsers: Array<{ userId: string; email: string; delta: string }>;
}

export interface PolicyAuditEvent {
  id: string;
  tenantId: string;
  policyId: string;
  action: string;
  actorId: string;
  actorEmail: string;
  beforeState?: Partial<Policy>;
  afterState?: Partial<Policy>;
  ip?: string;
  userAgent?: string;
  requestId: string;
  traceId: string;
  rowHash: string;
  createdAt: string;
}

export interface PolicyAuditListResponse {
  items: PolicyAuditEvent[];
  nextCursor?: string;
}

export interface AccessAuditEvent {
  id: string;
  tenantId: string;
  userId: string;
  userEmail: string;
  dataSourceId: string;
  resource: string;
  action: string;
  decision: "permit" | "deny";
  reason: string;
  rowCount?: number;
  queryHash?: string;
  durationMs: number;
  riskScore?: number;
  createdAt: string;
}

export interface AccessAuditListResponse {
  items: AccessAuditEvent[];
  nextCursor?: string;
}

export interface ApiErrorBody {
  code: string;
  message: string;
  requestId: string;
}
