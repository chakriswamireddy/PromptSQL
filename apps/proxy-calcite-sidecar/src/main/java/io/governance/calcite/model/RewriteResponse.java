package io.governance.calcite.model;

import com.fasterxml.jackson.annotation.JsonProperty;
import java.util.List;

public class RewriteResponse {

    @JsonProperty("rewritten_sql")
    private String rewrittenSql;

    @JsonProperty("referenced_tables")
    private List<String> referencedTables;

    @JsonProperty("referenced_columns")
    private List<String> referencedColumns;

    @JsonProperty("ast_hash")
    private String astHash;

    @JsonProperty("rewrite_duration_ms")
    private long rewriteDurationMs;

    @JsonProperty("error")
    private RewriteError error;

    // Builder-style setters.
    public RewriteResponse rewrittenSql(String v)              { this.rewrittenSql = v; return this; }
    public RewriteResponse referencedTables(List<String> v)    { this.referencedTables = v; return this; }
    public RewriteResponse referencedColumns(List<String> v)   { this.referencedColumns = v; return this; }
    public RewriteResponse astHash(String v)                   { this.astHash = v; return this; }
    public RewriteResponse rewriteDurationMs(long v)           { this.rewriteDurationMs = v; return this; }
    public RewriteResponse error(RewriteError v)               { this.error = v; return this; }

    public String getRewrittenSql()            { return rewrittenSql; }
    public List<String> getReferencedTables()  { return referencedTables; }
    public List<String> getReferencedColumns() { return referencedColumns; }
    public String getAstHash()                 { return astHash; }
    public long getRewriteDurationMs()         { return rewriteDurationMs; }
    public RewriteError getError()             { return error; }

    public static class RewriteError {
        @JsonProperty("code")    public String code;
        @JsonProperty("message") public String message;

        public RewriteError(String code, String message) {
            this.code = code;
            this.message = message;
        }
    }

    public static RewriteResponse denied(String code, String message) {
        return new RewriteResponse().error(new RewriteError(code, message));
    }
}
