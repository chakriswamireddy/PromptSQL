import { BaseClient } from "./client.js";

export interface LoginRequest {
  email: string;
  password: string;
  tenant_id: string;
}

export interface LoginResponse {
  access_token: string;
  refresh_token: string;
  expires_in: number;
  token_type: string;
}

export interface StepUpResponse extends LoginResponse {}

export class AuthClient extends BaseClient {
  async login(req: LoginRequest): Promise<LoginResponse> {
    const resp = await this.request<LoginResponse>("POST", "/v1/auth/login", req);
    this.setToken(resp.access_token);
    return resp;
  }

  async refresh(refreshToken: string): Promise<LoginResponse> {
    const resp = await this.request<LoginResponse>("POST", "/v1/auth/refresh", {
      refresh_token: refreshToken,
    });
    this.setToken(resp.access_token);
    return resp;
  }

  async logout(): Promise<void> {
    await this.request("POST", "/v1/auth/logout");
    this._token = undefined;
  }

  async initiateStepUp(obligationToken: string): Promise<void> {
    await this.request("POST", "/v1/auth/step-up/initiate", {
      obligation_token: obligationToken,
    });
  }

  async completeStepUp(
    obligationToken: string,
    mfaCode: string
  ): Promise<StepUpResponse> {
    return this.request<StepUpResponse>("POST", "/v1/auth/step-up/complete", {
      obligation_token: obligationToken,
      mfa_code: mfaCode,
    });
  }
}
