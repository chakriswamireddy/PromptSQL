export { GovernanceError } from "./client.js";
export type { ClientOptions, APIError } from "./client.js";

export { AuthClient } from "./auth.js";
export type { LoginRequest, LoginResponse } from "./auth.js";

export { PDPClient } from "./pdp.js";
export type {
  DecideRequest,
  DecideResponse,
  BulkDecideRequest,
  SubjectContext,
  ResourceContext,
  Obligation,
} from "./pdp.js";

export { WebhookClient, verifyWebhookSignature } from "./webhooks.js";
export type { WebhookSubscription } from "./webhooks.js";

import { AuthClient } from "./auth.js";
import { PDPClient } from "./pdp.js";
import { WebhookClient } from "./webhooks.js";
import type { ClientOptions } from "./client.js";

/**
 * GovernanceClient is the unified SDK entry point.
 *
 * @example
 * ```ts
 * const client = new GovernanceClient({ baseURL: "https://api.platform.io" });
 * const { access_token } = await client.auth.login({ email, password, tenant_id });
 * const decision = await client.pdp.decide({ ... });
 * ```
 */
export class GovernanceClient {
  readonly auth: AuthClient;
  readonly pdp: PDPClient;
  readonly webhooks: WebhookClient;

  constructor(opts: ClientOptions) {
    this.auth = new AuthClient(opts);
    this.pdp = new PDPClient(opts);
    this.webhooks = new WebhookClient(opts);
  }

  /** Propagate a token received externally to all sub-clients. */
  setToken(token: string): void {
    this.auth.setToken(token);
    this.pdp.setToken(token);
    this.webhooks.setToken(token);
  }
}
