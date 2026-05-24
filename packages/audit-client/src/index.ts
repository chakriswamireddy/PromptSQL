/**
 * Audit client — Phase 5 production implementation.
 * Replaces the Phase 0 no-op with a real batched Kafka producer.
 *
 * Fire-and-forget: callers await policyEvent/accessEvent/systemEvent but the
 * internal flush is async. Events are never on the critical request path.
 */

import { createHmac, randomUUID } from "crypto";
import * as fs from "fs";
import * as path from "path";
import { CompressionTypes, Kafka, Producer, Message } from "kafkajs";
import { Counter, Histogram, register } from "prom-client";
import {
  SCHEMA_V1,
  PolicyEventInput,
  AccessEventInput,
  SystemEventInput,
  PolicyEventPayload,
  AccessEventPayload,
  SystemEventPayload,
} from "./types.js";

export * from "./types.js";

// ── Metrics ──────────────────────────────────────────────────────────────────

const produced = new Counter({
  name: "audit_produced_total",
  help: "Total audit events successfully sent to Kafka.",
  labelNames: ["topic"],
  registers: [register],
});

const produceErrors = new Counter({
  name: "audit_produce_errors_total",
  help: "Total Kafka produce errors.",
  labelNames: ["topic"],
  registers: [register],
});

const buffered = new Counter({
  name: "audit_buffered_total",
  help: "Total events spooled to disk during Kafka outage.",
  labelNames: ["topic"],
  registers: [register],
});

const dropped = new Counter({
  name: "audit_dropped_total",
  help: "Total events dropped when disk buffer ceiling was reached.",
  labelNames: ["topic"],
  registers: [register],
});

const flushDuration = new Histogram({
  name: "audit_batch_flush_duration_seconds",
  help: "Time to flush one batch to Kafka.",
  labelNames: ["topic"],
  buckets: [0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5],
  registers: [register],
});

// ── Config ────────────────────────────────────────────────────────────────────

export interface AuditClientConfig {
  brokers: string[];
  topicPolicy: string;
  topicAccess: string;
  topicSystem: string;
  service: string;
  tenantId: string;
  actorId: string;
  /** Per-tenant HMAC key for actor tokenization (hex-encoded). */
  hmacKey?: string;
  /** Flush interval in ms. Default: 500. */
  flushIntervalMs?: number;
  /** Batch size in bytes that triggers an early flush. Default: 1 MB. */
  flushBytes?: number;
  /** Directory for disk spool on Kafka outage. */
  diskBufferDir?: string;
  /** Max disk spool size in bytes. Default: 256 MB. */
  diskBufferMaxBytes?: number;
  /** Gate the entire client. When false all calls are no-ops. Default: true. */
  enabled?: boolean;
}

// ── Client ────────────────────────────────────────────────────────────────────

export class AuditClient {
  private readonly cfg: Required<AuditClientConfig>;
  private readonly producer: Producer;
  private readonly batches: Record<string, Message[]> = {};
  private batchBytes = 0;
  private diskUsed = 0;
  private flushTimer?: ReturnType<typeof setInterval>;
  private closed = false;

  constructor(cfg: AuditClientConfig) {
    this.cfg = {
      flushIntervalMs: 500,
      flushBytes: 1 << 20,
      diskBufferDir: "",
      diskBufferMaxBytes: 256 << 20,
      enabled: true,
      hmacKey: "",
      ...cfg,
    };

    const kafka = new Kafka({
      clientId: `audit-client-${cfg.service}`,
      brokers: cfg.brokers,
    });
    this.producer = kafka.producer({
      idempotent: true,
      transactionTimeout: 30000,
    });

    for (const topic of [cfg.topicPolicy, cfg.topicAccess, cfg.topicSystem]) {
      this.batches[topic] = [];
    }

    if (this.cfg.enabled) {
      this.producer.connect().catch(() => {});
      this.flushTimer = setInterval(() => {
        void this.flush();
      }, this.cfg.flushIntervalMs);
    }
  }

  async policyEvent(
    tenantId: string,
    actorId: string,
    ev: PolicyEventInput,
  ): Promise<void> {
    if (!this.cfg.enabled) return;
    const payload: PolicyEventPayload = {
      ...ev,
      eventId: randomUUID(),
      schema: SCHEMA_V1,
      service: this.cfg.service,
      eventTime: new Date().toISOString(),
      tenantId,
      actorId,
      actorToken: this.tokenize(tenantId, actorId),
    };
    this.enqueue(this.cfg.topicPolicy, tenantId, payload);
  }

  async accessEvent(ev: AccessEventInput): Promise<void> {
    if (!this.cfg.enabled) return;
    const payload: AccessEventPayload = {
      ...ev,
      eventId: randomUUID(),
      schema: SCHEMA_V1,
      service: this.cfg.service,
      eventTime: new Date().toISOString(),
      actorToken: this.tokenize(ev.tenantId, ev.userId),
    };
    this.enqueue(this.cfg.topicAccess, ev.userId, payload);
  }

  async systemEvent(ev: SystemEventInput): Promise<void> {
    if (!this.cfg.enabled) return;
    const payload: SystemEventPayload = {
      ...ev,
      eventId: randomUUID(),
      schema: SCHEMA_V1,
      service: this.cfg.service,
      eventTime: new Date().toISOString(),
    };
    this.enqueue(this.cfg.topicSystem, "", payload);
  }

  private enqueue(topic: string, key: string, payload: unknown): void {
    const value = Buffer.from(JSON.stringify(payload));
    this.batches[topic].push({ key: key || undefined, value });
    this.batchBytes += value.length;
    if (this.batchBytes >= this.cfg.flushBytes) {
      void this.flush();
    }
  }

  async flush(): Promise<void> {
    if (this.closed) return;
    const topics = [
      this.cfg.topicPolicy,
      this.cfg.topicAccess,
      this.cfg.topicSystem,
    ];
    for (const topic of topics) {
      await this.flushTopic(topic);
    }
    this.batchBytes = 0;
  }

  private async flushTopic(topic: string): Promise<void> {
    const msgs = this.batches[topic];
    if (!msgs || msgs.length === 0) return;
    this.batches[topic] = [];

    const start = Date.now();
    try {
      await this.producer.send({
        topic,
        compression: CompressionTypes.GZIP,
        messages: msgs,
      });
      produced.labels(topic).inc(msgs.length);
      flushDuration.labels(topic).observe((Date.now() - start) / 1000);
    } catch {
      produceErrors.labels(topic).inc(msgs.length);
      this.spillToDisk(topic, msgs);
    }
  }

  private spillToDisk(topic: string, msgs: Message[]): void {
    if (!this.cfg.diskBufferDir) {
      dropped.labels(topic).inc(msgs.length);
      return;
    }
    const data = Buffer.from(msgs.map((m) => m.value?.toString()).join("\n") + "\n");
    if (this.diskUsed + data.length > this.cfg.diskBufferMaxBytes) {
      dropped.labels(topic).inc(msgs.length);
      return;
    }
    const dir = path.join(this.cfg.diskBufferDir, topic.replace(/\./g, "_"));
    fs.mkdirSync(dir, { recursive: true });
    const fname = path.join(dir, `${Date.now()}.jsonl`);
    try {
      fs.writeFileSync(fname, data);
      this.diskUsed += data.length;
      buffered.labels(topic).inc(msgs.length);
    } catch {
      dropped.labels(topic).inc(msgs.length);
    }
  }

  private tokenize(tenantId: string, actorId: string): string {
    if (!this.cfg.hmacKey) return "";
    return createHmac("sha256", Buffer.from(this.cfg.hmacKey, "hex"))
      .update(`${tenantId}:${actorId}`)
      .digest("hex");
  }

  async close(): Promise<void> {
    this.closed = true;
    if (this.flushTimer) clearInterval(this.flushTimer);
    await this.flush();
    await this.producer.disconnect();
  }
}

// ── Factory ───────────────────────────────────────────────────────────────────

export function createAuditClient(cfg: AuditClientConfig): AuditClient {
  return new AuditClient(cfg);
}

// ── No-op fallback (kept for tests / disabled mode) ──────────────────────────

export class NoOpAuditClient extends AuditClient {
  constructor() {
    super({
      brokers: [],
      topicPolicy: "",
      topicAccess: "",
      topicSystem: "",
      service: "noop",
      tenantId: "",
      actorId: "",
      enabled: false,
    });
  }
}
