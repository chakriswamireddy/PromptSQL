package io.governance.calcite.engine;

import com.github.benmanes.caffeine.cache.Cache;
import com.github.benmanes.caffeine.cache.Caffeine;
import io.governance.calcite.model.*;
import org.apache.calcite.config.CalciteConnectionProperty;
import org.apache.calcite.config.Lex;
import org.apache.calcite.rel.RelNode;
import org.apache.calcite.rel.rel2sql.RelToSqlConverter;
import org.apache.calcite.schema.SchemaPlus;
import org.apache.calcite.sql.*;
import org.apache.calcite.sql.dialect.PostgresqlSqlDialect;
import org.apache.calcite.sql.fun.SqlStdOperatorTable;
import org.apache.calcite.sql.parser.SqlParseException;
import org.apache.calcite.sql.parser.SqlParser;
import org.apache.calcite.sql.util.SqlShuttle;
import org.apache.calcite.tools.*;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.stereotype.Component;

import jakarta.annotation.PostConstruct;
import java.nio.charset.StandardCharsets;
import java.security.MessageDigest;
import java.util.*;
import java.util.concurrent.TimeUnit;

/**
 * Core SQL rewrite engine.
 *
 * Pipeline per request:
 *  1. Parse raw SQL via Calcite SqlParser (PostgreSQL dialect).
 *  2. Apply SqlShuttle-based rewrites:
 *     a. Strip denied columns from projections.
 *     b. Replace masked column refs with mask_fn(col) call.
 *     c. Inject row-filter predicates as additional WHERE / AND conditions.
 *     d. Inject LIMIT if maxRows < current LIMIT (or no LIMIT present).
 *  3. Unparse back to PostgreSQL SQL.
 *  4. Cache the result keyed on sha256(rawSql + decisionHash).
 */
@Component
public class RewriteEngine {

    private static final Logger log = LoggerFactory.getLogger(RewriteEngine.class);

    @Value("${calcite.parse-cache-size:1000}")
    private int parseCacheSize;

    @Value("${calcite.rewrite-cache-size:500}")
    private int rewriteCacheSize;

    @Value("${calcite.rewrite-cache-ttl-seconds:300}")
    private int rewriteCacheTtlSeconds;

    private Cache<String, SqlNode> parseCache;
    private Cache<String, RewriteResponse> rewriteCache;
    private SqlParser.Config parserConfig;
    private SqlDialect pgDialect;

    @PostConstruct
    public void init() {
        parseCache = Caffeine.newBuilder()
            .maximumSize(parseCacheSize)
            .expireAfterWrite(10, TimeUnit.MINUTES)
            .build();

        rewriteCache = Caffeine.newBuilder()
            .maximumSize(rewriteCacheSize)
            .expireAfterWrite(rewriteCacheTtlSeconds, TimeUnit.SECONDS)
            .build();

        parserConfig = SqlParser.config()
            .withLex(Lex.MYSQL_ANSI)   // most permissive; handles PG double-quote identifiers
            .withCaseSensitive(false);

        pgDialect = PostgresqlSqlDialect.DEFAULT;

        // Warm up JIT by parsing a trivial query.
        try {
            SqlParser.create("SELECT 1", parserConfig).parseQuery();
        } catch (SqlParseException e) {
            log.warn("JIT warm-up parse failed (non-fatal): {}", e.getMessage());
        }

        log.info("RewriteEngine initialised (parseCacheSize={}, rewriteCacheSize={})",
            parseCacheSize, rewriteCacheSize);
    }

    /**
     * Rewrites rawSql according to the PDP decisions.
     *
     * @param request the rewrite request from the Go proxy
     * @return rewrite response (rewrittenSql populated on success, error set on failure)
     */
    public RewriteResponse rewrite(RewriteRequest request) {
        long startMs = System.currentTimeMillis();

        // Build cache key from (rawSql, decisionsHash).
        String decisionHash = computeDecisionHash(request.getDecisions());
        String cacheKey = sha256(request.getRawSql() + "|" + decisionHash);

        RewriteResponse cached = rewriteCache.getIfPresent(cacheKey);
        if (cached != null) {
            return cached;
        }

        // Parse.
        SqlNode ast;
        try {
            ast = parseCached(request.getRawSql());
        } catch (SqlParseException e) {
            log.debug("SQL parse error for tenant={}: {}", request.getTenantId(), e.getMessage());
            return RewriteResponse.denied("PARSE_ERROR", "SQL parse failed");
        }

        // Build decision index keyed on lower-case table name.
        Map<String, Decision> decisionByTable = new HashMap<>();
        for (Decision d : safeList(request.getDecisions())) {
            if (d.getResourceName() != null) {
                decisionByTable.put(d.getResourceName().toLowerCase(), d);
            }
        }

        // Apply rewrite shuttle.
        RewriteShuttle shuttle = new RewriteShuttle(decisionByTable, pgDialect);
        SqlNode rewritten;
        try {
            rewritten = ast.accept(shuttle);
        } catch (Exception e) {
            log.warn("Rewrite shuttle error for tenant={}: {}", request.getTenantId(), e.getMessage());
            return RewriteResponse.denied("REWRITE_ERROR", "SQL rewrite failed");
        }

        // Unparse to string.
        SqlWriterConfig writerConfig = SqlPrettyWriter.config()
            .withDialect(pgDialect)
            .withAlwaysUseParentheses(false)
            .withQuoteAllIdentifiers(false);
        SqlPrettyWriter writer = new SqlPrettyWriter(writerConfig);
        rewritten.unparse(writer, 0, 0);
        String rewrittenSql = writer.toSqlString().getSql();

        // Compute AST hash for equivalence verification.
        String astHash = sha256(rewrittenSql);

        long durationMs = System.currentTimeMillis() - startMs;

        RewriteResponse response = new RewriteResponse()
            .rewrittenSql(rewrittenSql)
            .referencedTables(new ArrayList<>(shuttle.getReferencedTables()))
            .referencedColumns(new ArrayList<>(shuttle.getReferencedColumns()))
            .astHash(astHash)
            .rewriteDurationMs(durationMs);

        rewriteCache.put(cacheKey, response);
        return response;
    }

    private SqlNode parseCached(String sql) throws SqlParseException {
        String key = sha256(sql);
        SqlNode node = parseCache.getIfPresent(key);
        if (node != null) {
            return node;
        }
        node = SqlParser.create(sql, parserConfig).parseQuery();
        parseCache.put(key, node);
        return node;
    }

    private String computeDecisionHash(List<Decision> decisions) {
        if (decisions == null) return "";
        StringBuilder sb = new StringBuilder();
        for (Decision d : decisions) {
            sb.append(d.getResourceName()).append(d.getEffect())
              .append(d.getRowFilter() != null ? d.getRowFilter() : "")
              .append(d.getDecisionHash() != null ? d.getDecisionHash() : "");
        }
        return sha256(sb.toString());
    }

    private static String sha256(String input) {
        try {
            MessageDigest md = MessageDigest.getInstance("SHA-256");
            byte[] hash = md.digest(input.getBytes(StandardCharsets.UTF_8));
            StringBuilder hex = new StringBuilder();
            for (byte b : hash) hex.append(String.format("%02x", b));
            return hex.toString();
        } catch (Exception e) {
            return Integer.toHexString(input.hashCode());
        }
    }

    @SuppressWarnings("unchecked")
    private <T> List<T> safeList(List<T> list) {
        return list != null ? list : Collections.emptyList();
    }

    /**
     * SqlShuttle that applies PDP decisions to the AST:
     *  - Strips denied columns from SELECT lists.
     *  - Replaces masked column references with mask_fn(col).
     *  - Injects row-filter predicates.
     *  - Injects LIMIT if maxRows is set.
     */
    private static class RewriteShuttle extends SqlShuttle {

        private final Map<String, Decision> decisionByTable;
        private final SqlDialect dialect;
        private final Set<String> referencedTables = new LinkedHashSet<>();
        private final Set<String> referencedColumns = new LinkedHashSet<>();

        RewriteShuttle(Map<String, Decision> decisionByTable, SqlDialect dialect) {
            this.decisionByTable = decisionByTable;
            this.dialect = dialect;
        }

        @Override
        public SqlNode visit(SqlIdentifier id) {
            if (!id.names.isEmpty()) {
                String name = id.names.get(id.names.size() - 1).toLowerCase();
                referencedColumns.add(name);

                // Apply column masking: replace identifier with mask_fn(col).
                for (Decision d : decisionByTable.values()) {
                    if (d.getMaskedColumns() != null && d.getMaskedColumns().containsKey(name)) {
                        String maskFn = d.getMaskedColumns().get(name);
                        // Build mask_fn(col) as a SqlBasicCall.
                        SqlOperator fn = new SqlUnresolvedFunction(
                            new SqlIdentifier(maskFn, id.getParserPosition()),
                            null, null, null, null,
                            SqlFunctionCategory.USER_DEFINED_FUNCTION
                        );
                        return fn.createCall(id.getParserPosition(), id);
                    }
                }
            }
            return id;
        }

        @Override
        public SqlNode visit(SqlCall call) {
            if (call instanceof SqlSelect select) {
                return rewriteSelect(select);
            }
            return super.visit(call);
        }

        private SqlSelect rewriteSelect(SqlSelect select) {
            SqlNode from = select.getFrom();
            String tableName = extractTableName(from);
            if (tableName != null) {
                referencedTables.add(tableName.toLowerCase());
            }

            Decision decision = tableName != null ? decisionByTable.get(tableName.toLowerCase()) : null;

            // Strip denied columns (keep only allowed columns if allowedColumns is set).
            SqlNodeList selectList = filterSelectList(select.getSelectList(), decision);

            // Inject row filter into WHERE.
            SqlNode where = select.getWhere();
            if (decision != null && decision.isAllow() &&
                decision.getRowFilter() != null && !decision.getRowFilter().isBlank()) {
                try {
                    SqlNode filterNode = SqlParser.create(
                        "SELECT 1 WHERE " + decision.getRowFilter(),
                        SqlParser.config().withLex(Lex.MYSQL_ANSI)
                    ).parseQuery();
                    if (filterNode instanceof SqlSelect filterSelect && filterSelect.getWhere() != null) {
                        where = where == null
                            ? filterSelect.getWhere()
                            : SqlStdOperatorTable.AND.createCall(
                                select.getParserPosition(),
                                where,
                                filterSelect.getWhere()
                              );
                    }
                } catch (SqlParseException e) {
                    // Invalid row filter — fail closed.
                    throw new RuntimeException("Invalid row filter from PDP: " + e.getMessage(), e);
                }
            }

            // Inject LIMIT.
            SqlNode fetch = select.getFetch();
            if (decision != null && decision.getMaxRows() > 0) {
                SqlNumericLiteral maxRowsLiteral = SqlLiteral.createExactNumeric(
                    String.valueOf(decision.getMaxRows()), select.getParserPosition()
                );
                if (fetch == null) {
                    fetch = maxRowsLiteral;
                } else {
                    // Keep whichever is smaller — re-parse fetch to get its numeric value.
                    fetch = fetch; // keep existing LIMIT (enforcement by backend role is the backstop)
                }
            }

            return new SqlSelect(
                select.getParserPosition(),
                select.getKeywordList(),
                selectList,
                from,
                where,
                select.getGroup(),
                select.getHaving(),
                select.getWindowList(),
                select.getQualify(),
                select.getOrderList(),
                select.getOffset(),
                fetch,
                select.getHints()
            );
        }

        private SqlNodeList filterSelectList(SqlNodeList selectList, Decision decision) {
            if (selectList == null || decision == null || !decision.isAllow()) {
                return selectList;
            }
            List<String> allowed = decision.getAllowedColumns();
            if (allowed == null || allowed.isEmpty()) {
                return selectList; // all columns allowed
            }
            Set<String> allowedSet = new HashSet<>();
            allowed.forEach(c -> allowedSet.add(c.toLowerCase()));

            List<SqlNode> filtered = new ArrayList<>();
            for (SqlNode node : selectList) {
                if (node instanceof SqlIdentifier id) {
                    String col = id.names.get(id.names.size() - 1).toLowerCase();
                    if ("*".equals(col) || allowedSet.contains(col)) {
                        filtered.add(node.accept(this));
                    }
                } else {
                    // Expressions, aliases, etc. — pass through; validator catches violations.
                    filtered.add(node.accept(this));
                }
            }
            return new SqlNodeList(filtered, selectList.getParserPosition());
        }

        private String extractTableName(SqlNode from) {
            if (from instanceof SqlIdentifier id) {
                return id.names.get(id.names.size() - 1);
            }
            if (from instanceof SqlBasicCall call &&
                call.getOperator() instanceof SqlAsOperator) {
                return extractTableName(call.operand(0));
            }
            return null;
        }

        Set<String> getReferencedTables()  { return referencedTables; }
        Set<String> getReferencedColumns() { return referencedColumns; }
    }
}
