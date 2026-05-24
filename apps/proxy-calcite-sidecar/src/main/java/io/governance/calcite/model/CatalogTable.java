package io.governance.calcite.model;

import com.fasterxml.jackson.annotation.JsonProperty;
import java.util.List;

public class CatalogTable {

    @JsonProperty("schema")
    private String schema;

    @JsonProperty("table")
    private String table;

    @JsonProperty("columns")
    private List<String> columns;

    public String getSchema()       { return schema; }
    public String getTable()        { return table; }
    public List<String> getColumns() { return columns; }
}
