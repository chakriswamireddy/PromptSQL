import { z } from 'zod';

// ── AllowedSnapshot ────────────────────────────────────────────────────────────

export const SnapshotColumnSchema = z.object({
  name: z.string(),
  type: z.string(),
  nullable: z.boolean().optional(),
  masked: z.string().optional(),
  classification: z.enum(['public', 'internal', 'confidential', 'restricted']).optional(),
  description: z.string().optional(),
  sample_values: z.array(z.string()).optional(),
});
export type SnapshotColumn = z.infer<typeof SnapshotColumnSchema>;

export const SnapshotFKSchema = z.object({
  column: z.string(),
  ref_table: z.string(),
  ref_column: z.string(),
});
export type SnapshotFK = z.infer<typeof SnapshotFKSchema>;

export const SnapshotTableSchema = z.object({
  name: z.string(),
  schema: z.string(),
  description: z.string().optional(),
  columns: z.array(SnapshotColumnSchema),
  foreign_keys: z.array(SnapshotFKSchema).optional(),
  row_filter_summary: z.string().optional(),
});
export type SnapshotTable = z.infer<typeof SnapshotTableSchema>;

export const AllowedSnapshotSchema = z.object({
  version: z.string(),
  schemaVersion: z.string(),
  policySetVersion: z.string(),
  dataSourceId: z.string(),
  tables: z.array(SnapshotTableSchema),
  truncated: z.boolean().optional(),
});
export type AllowedSnapshot = z.infer<typeof AllowedSnapshotSchema>;

// ── Doc Retrieval ─────────────────────────────────────────────────────────────

export const Classification = z.enum(['public', 'internal', 'confidential', 'restricted']);
export type Classification = z.infer<typeof Classification>;

export const ChunkResultSchema = z.object({
  id: z.string(),
  corpus_id: z.string(),
  chunk_text: z.string(),
  wrapped_text: z.string(),
  classification: Classification,
  similarity: z.number(),
  metadata: z.record(z.unknown()).optional(),
  injection_triggers: z.array(z.string()).optional(),
  truncated: z.boolean().optional(),
});
export type ChunkResult = z.infer<typeof ChunkResultSchema>;

export const ProviderRouteSchema = z.object({
  ProviderName: z.string(),
  Model: z.string(),
  ZeroRetention: z.boolean(),
  PrivateOnly: z.boolean(),
  ResidencyRegion: z.string().optional(),
  ContentClassification: Classification,
});
export type ProviderRoute = z.infer<typeof ProviderRouteSchema>;

export const DocsResponseSchema = z.object({
  chunks: z.array(ChunkResultSchema),
  content_classification: Classification,
  provider_route: ProviderRouteSchema,
  query_hash: z.string(),
  policy_set_version: z.string(),
  snapshot_hash: z.string().optional(),
});
export type DocsResponse = z.infer<typeof DocsResponseSchema>;

// ── Requests ──────────────────────────────────────────────────────────────────

export interface SnapshotRequest {
  data_source_id: string;
}

export interface DocsRequest {
  query: string;
  top_k?: number;
  data_source_ids?: string[];
  min_similarity?: number;
}

export interface RouteRequest {
  content_classifications: Classification[];
}

export interface ExplainRequest {
  data_source_id: string;
  query: string;
}

// ── Session context (passed as headers) ──────────────────────────────────────

export interface SessionHeaders {
  tenantId: string;
  userId: string;
  userRoles: string[];
  subjectDepartment?: string;
}
