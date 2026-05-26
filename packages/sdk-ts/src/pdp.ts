import { BaseClient } from "./client.js";

export interface SubjectContext {
  user_id: string;
  roles: string[];
  attrs?: Record<string, string>;
}

export interface ResourceContext {
  type: string;
  id: string;
  attributes?: Record<string, string>;
}

export interface DecideRequest {
  tenant_id: string;
  subject: SubjectContext;
  resource: ResourceContext;
  action: string;
  environment?: Record<string, unknown>;
}

export interface Obligation {
  type: string;
  payload?: Record<string, unknown>;
}

export interface DecideResponse {
  decision: "allow" | "deny";
  obligations?: Obligation[];
  column_masks?: Record<string, string>;
  row_filter?: string;
  reason?: string;
  trace_id?: string;
}

export interface BulkDecideRequest {
  tenant_id: string;
  subject: SubjectContext;
  requests: DecideRequest[];
}

export class PDPClient extends BaseClient {
  async decide(req: DecideRequest): Promise<DecideResponse> {
    return this.request<DecideResponse>("POST", "/v1/pdp/decide", req);
  }

  async bulkDecide(req: BulkDecideRequest): Promise<DecideResponse[]> {
    const resp = await this.request<{ results: DecideResponse[] }>(
      "POST",
      "/v1/pdp/bulk-decide",
      req
    );
    return resp.results;
  }

  async explain(req: DecideRequest): Promise<string> {
    const resp = await this.request<{ explanation: string }>(
      "POST",
      "/v1/pdp/explain",
      req
    );
    return resp.explanation;
  }
}
