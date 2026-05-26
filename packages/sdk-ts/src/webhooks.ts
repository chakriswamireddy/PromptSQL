import { BaseClient } from "./client.js";

export interface WebhookSubscription {
  id: string;
  tenant_id: string;
  url: string;
  event_types: string[];
  active: boolean;
  created_at: string;
}

export class WebhookClient extends BaseClient {
  async create(
    tenantId: string,
    url: string,
    eventTypes: string[]
  ): Promise<WebhookSubscription> {
    return this.request<WebhookSubscription>(
      "POST",
      `/v1/admin/${tenantId}/webhooks`,
      { url, event_types: eventTypes }
    );
  }

  async list(tenantId: string): Promise<WebhookSubscription[]> {
    const resp = await this.request<{ subscriptions: WebhookSubscription[] }>(
      "GET",
      `/v1/admin/${tenantId}/webhooks`
    );
    return resp.subscriptions;
  }

  async delete(tenantId: string, subscriptionId: string): Promise<void> {
    await this.request("DELETE", `/v1/admin/${tenantId}/webhooks/${subscriptionId}`);
  }
}

/**
 * Verifies the HMAC-SHA256 signature on an incoming webhook payload.
 * Works in both Node.js (crypto module) and browser (SubtleCrypto).
 *
 * @param payload  - Raw request body as Uint8Array or string
 * @param signature - Value of the X-Governance-Signature header ("sha256=<hex>")
 * @param secret   - Webhook secret returned at subscription creation
 */
export async function verifyWebhookSignature(
  payload: Uint8Array | string,
  signature: string,
  secret: string
): Promise<boolean> {
  const [algo, hex] = signature.split("=", 2);
  if (algo !== "sha256" || !hex) return false;

  const enc = new TextEncoder();
  const keyData = typeof secret === "string" ? enc.encode(secret) : secret;
  const payloadData =
    typeof payload === "string" ? enc.encode(payload) : payload;

  const key = await crypto.subtle.importKey(
    "raw",
    keyData,
    { name: "HMAC", hash: "SHA-256" },
    false,
    ["verify"]
  );

  const sigBytes = hexToBytes(hex);
  return crypto.subtle.verify("HMAC", key, sigBytes, payloadData);
}

function hexToBytes(hex: string): Uint8Array {
  const arr = new Uint8Array(hex.length / 2);
  for (let i = 0; i < hex.length; i += 2) {
    arr[i / 2] = parseInt(hex.slice(i, i + 2), 16);
  }
  return arr;
}
