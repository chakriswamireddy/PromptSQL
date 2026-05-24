import { initialize, isEnabled, getVariant, destroy } from "unleash-client";

export interface FeatureFlagConfig {
  unleashUrl: string;
  apiToken: string;
  appName: string;
  environment: string;
}

/**
 * Initialise the Unleash client. Must be called once before any isEnabled check.
 * Resolves when the first flag poll completes.
 */
export async function initFeatureFlags(cfg: FeatureFlagConfig): Promise<void> {
  const client = initialize({
    url: cfg.unleashUrl,
    appName: cfg.appName,
    customHeaders: { Authorization: cfg.apiToken },
    environment: cfg.environment,
  });
  await new Promise<void>((resolve, reject) => {
    client.on("ready", resolve);
    client.on("error", reject);
    setTimeout(resolve, 5000); // graceful degradation if Unleash is unreachable
  });
}

/**
 * Check whether a feature flag is enabled. Optionally pass a tenantId to
 * activate tenant-scoped Unleash strategies.
 */
export function isFlagEnabled(flag: string, tenantId?: string): boolean {
  return isEnabled(flag, tenantId ? { properties: { tenantId } } : undefined);
}

export { getVariant, destroy as shutdownFeatureFlags };
