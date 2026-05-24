import { z } from "zod";

/**
 * SessionContext — the authoritative shape of the authenticated session
 * propagated through every service call. The Go counterpart is generated from
 * this schema via: pnpm --filter @platform/shared-types run codegen
 *
 * INVARIANTS:
 * - Roles and attributes are NEVER sourced from JWT claims; they are resolved
 *   freshly from PostgreSQL per request (60 s Redis cache).
 * - riskScore is null until Phase 13 populates it.
 * - mfaAt / amr are copied from the JWT for obligation checks.
 */

export const DeviceTrustSchema = z.enum(["managed", "byod", "unknown"]);
export const NetworkTrustSchema = z.enum(["corp", "vpn", "public"]);
export const SubjectKindSchema = z.enum(["user", "service", "apikey"]);

export const SessionAttributesSchema = z.object({
  department: z.string().optional(),
  campusId: z.string().uuid().optional(),
  region: z.string().optional(),
  clearanceLevel: z.number().int().min(0).max(10).optional(),
  mfaSince: z.date().optional(),
  deviceTrust: DeviceTrustSchema.default("unknown"),
  networkTrust: NetworkTrustSchema.default("public"),
});

export const SessionContextSchema = z.object({
  userId: z.string().uuid(),
  tenantId: z.string().uuid(),
  sessionId: z.string().uuid(),
  subjectKind: SubjectKindSchema.default("user"),
  roles: z.array(z.string()),
  attributes: SessionAttributesSchema,
  requestId: z.string().uuid(),
  traceId: z.string(),
  parentSpanId: z.string().optional(),
  isBreakGlass: z.boolean().default(false),
  // Populated from Phase 13 anomaly detection; null until then.
  riskScore: z.number().int().min(0).max(100).nullable().optional(),
  issuedAt: z.date(),
  expiresAt: z.date(),
  // JWT authentication method references and MFA timestamp.
  amr: z.array(z.string()).optional(),
  mfaAt: z.date().optional(),
});

export type DeviceTrust = z.infer<typeof DeviceTrustSchema>;
export type NetworkTrust = z.infer<typeof NetworkTrustSchema>;
export type SubjectKind = z.infer<typeof SubjectKindSchema>;
export type SessionAttributes = z.infer<typeof SessionAttributesSchema>;
export type SessionContext = z.infer<typeof SessionContextSchema>;

/**
 * StructuredError — canonical RFC 7807 problem+json error shape for all API responses.
 */
export const StructuredErrorSchema = z.object({
  code: z.string(),
  message: z.string(),
  requestId: z.string().uuid(),
  details: z.record(z.unknown()).optional(),
});

export type StructuredError = z.infer<typeof StructuredErrorSchema>;

/**
 * JWTClaims — identity-only claims in the Ed25519 access token.
 * Roles, permissions, and attributes are intentionally absent.
 */
export const JWTClaimsSchema = z.object({
  iss: z.string(),
  aud: z.union([z.string(), z.array(z.string())]),
  sub: z.string().uuid(),  // userID
  tenant: z.string().uuid(),
  session_id: z.string().uuid(),
  amr: z.array(z.string()).optional(),
  mfa_at: z.number().int().optional(),
  iat: z.number().int(),
  exp: z.number().int(),
  jti: z.string().uuid(),
});

export type JWTClaims = z.infer<typeof JWTClaimsSchema>;

/**
 * TokenResponse — shape returned by POST /v1/auth/login and POST /v1/auth/refresh.
 */
export const TokenResponseSchema = z.object({
  accessToken: z.string(),
  refreshToken: z.string(),
  expiresIn: z.number().int(),
  tokenType: z.literal("Bearer"),
});

export type TokenResponse = z.infer<typeof TokenResponseSchema>;

/**
 * LoginRequest — body for POST /v1/auth/login.
 */
export const LoginRequestSchema = z.object({
  tenantSlug: z.string().min(1),
  email: z.string().email(),
  password: z.string().min(1),
  totpCode: z.string().length(6).optional(),
});

export type LoginRequest = z.infer<typeof LoginRequestSchema>;
