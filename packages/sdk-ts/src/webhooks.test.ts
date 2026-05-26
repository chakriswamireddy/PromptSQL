import { describe, expect, it } from "vitest";
import { verifyWebhookSignature } from "./webhooks.js";
import { createHmac } from "node:crypto";

const SECRET = "test-secret-abc123";
const PAYLOAD = JSON.stringify({ event: "policy.created" });

function sign(payload: string, secret: string): string {
  const mac = createHmac("sha256", secret).update(payload).digest("hex");
  return `sha256=${mac}`;
}

describe("verifyWebhookSignature", () => {
  it("returns true for a valid signature", async () => {
    const sig = sign(PAYLOAD, SECRET);
    const ok = await verifyWebhookSignature(PAYLOAD, sig, SECRET);
    expect(ok).toBe(true);
  });

  it("returns false for a tampered payload", async () => {
    const sig = sign(PAYLOAD, SECRET);
    const ok = await verifyWebhookSignature(PAYLOAD + "tampered", sig, SECRET);
    expect(ok).toBe(false);
  });

  it("returns false for a bad signature format", async () => {
    const ok = await verifyWebhookSignature(PAYLOAD, "md5=notvalid", SECRET);
    expect(ok).toBe(false);
  });

  it("returns false for wrong secret", async () => {
    const sig = sign(PAYLOAD, "wrong-secret");
    const ok = await verifyWebhookSignature(PAYLOAD, sig, SECRET);
    expect(ok).toBe(false);
  });
});
