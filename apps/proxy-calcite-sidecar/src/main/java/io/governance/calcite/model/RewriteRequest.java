package io.governance.calcite.model;

import com.fasterxml.jackson.annotation.JsonProperty;
import java.util.List;
import java.util.Map;

/**
 * JSON-mapped request from the Go proxy.
 * Mirrors pkg/calcitepb.RewriteRequest.
 */
public class RewriteRequest {

    @JsonProperty("raw_sql")
    private String rawSql;

    @JsonProperty("source_dialect")
    private String sourceDialect;

    @JsonProperty("target_dialect")
    private String targetDialect;

    @JsonProperty("decisions")
    private List<Decision> decisions;

    @JsonProperty("catalog")
    private List<CatalogTable> catalog;

    @JsonProperty("binds")
    private Map<String, String> binds;

    @JsonProperty("tenant_id")
    private String tenantId;

    @JsonProperty("user_id")
    private String userId;

    @JsonProperty("request_id")
    private String requestId;

    // Getters
    public String getRawSql()            { return rawSql; }
    public String getSourceDialect()     { return sourceDialect; }
    public String getTargetDialect()     { return targetDialect; }
    public List<Decision> getDecisions() { return decisions; }
    public List<CatalogTable> getCatalog() { return catalog; }
    public Map<String, String> getBinds() { return binds; }
    public String getTenantId()          { return tenantId; }
    public String getUserId()            { return userId; }
    public String getRequestId()         { return requestId; }
}
