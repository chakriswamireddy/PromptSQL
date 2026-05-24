package io.governance.calcite.model;

import com.fasterxml.jackson.annotation.JsonProperty;
import java.util.List;
import java.util.Map;

public class Decision {

    @JsonProperty("resource_type")
    private String resourceType;

    @JsonProperty("resource_name")
    private String resourceName;

    @JsonProperty("effect")
    private String effect; // "ALLOW" | "DENY"

    @JsonProperty("row_filter")
    private String rowFilter;

    @JsonProperty("allowed_columns")
    private List<String> allowedColumns;

    @JsonProperty("masked_columns")
    private Map<String, String> maskedColumns; // col → mask_fn

    @JsonProperty("max_rows")
    private long maxRows;

    @JsonProperty("decision_hash")
    private String decisionHash;

    public String getResourceType()            { return resourceType; }
    public String getResourceName()            { return resourceName; }
    public String getEffect()                  { return effect; }
    public String getRowFilter()               { return rowFilter; }
    public List<String> getAllowedColumns()     { return allowedColumns; }
    public Map<String, String> getMaskedColumns() { return maskedColumns; }
    public long getMaxRows()                   { return maxRows; }
    public String getDecisionHash()            { return decisionHash; }

    public boolean isAllow() { return "ALLOW".equalsIgnoreCase(effect); }
}
