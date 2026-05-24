import type { PepGraphState, SqlAst, ExprNode, ValidationError, AllowedSnapshot } from '../../pep-schemas';
import { pepAstValidationFailuresTotal } from '../../../metrics';

// AST Validator — the trust boundary.
// Pure deterministic checks; no LLM involved.
// Any failure here means the query is rejected.

// Default function allowlist per tenant tier (tenant can extend via config)
const DEFAULT_FUNCTION_ALLOWLIST = new Set([
  'sum', 'count', 'avg', 'min', 'max',
  'coalesce', 'nullif', 'greatest', 'least',
  'date_trunc', 'date_part', 'extract',
  'to_char', 'to_date', 'to_timestamp',
  'lower', 'upper', 'trim', 'length', 'substring',
  'abs', 'round', 'floor', 'ceil', 'mod',
  'concat', 'concat_ws',
  'now', 'current_date', 'current_timestamp',
  'cast',
]);

// Renders AST to SQL string for proxy submission (simplified renderer)
export function renderAstToSql(ast: SqlAst): string {
  const select = ast.select.map(renderSelectItem).join(', ');
  const from   = `${ast.from.schema}.${ast.from.name}${ast.from.alias ? ` ${ast.from.alias}` : ''}`;
  const joins  = (ast.joins ?? []).map((j) =>
    `${j.join_type} JOIN ${j.table.schema}.${j.table.name}${j.table.alias ? ` ${j.table.alias}` : ''} ON ${renderExpr(j.on)}`
  ).join('\n');
  const where  = ast.where ? `WHERE ${renderExpr(ast.where)}` : '';
  const group  = ast.group_by?.length ? `GROUP BY ${ast.group_by.map(renderExpr).join(', ')}` : '';
  const having = ast.having ? `HAVING ${renderExpr(ast.having)}` : '';
  const order  = ast.order_by?.length
    ? `ORDER BY ${ast.order_by.map((o) => `${renderExpr(o.expr)} ${o.dir}${o.nulls ? ` NULLS ${o.nulls}` : ''}`).join(', ')}`
    : '';
  const limit  = `LIMIT ${ast.limit}`;
  const offset = ast.offset ? `OFFSET ${ast.offset}` : '';

  return [
    `SELECT${ast.distinct ? ' DISTINCT' : ''} ${select}`,
    `FROM ${from}`,
    joins,
    where,
    group,
    having,
    order,
    limit,
    offset,
  ].filter(Boolean).join('\n');
}

function renderSelectItem(item: SqlAst['select'][number]): string {
  if (item.kind === 'Column') {
    const col = `${item.table}.${item.column}`;
    return item.alias ? `${col} AS ${item.alias}` : col;
  }
  if (item.kind === 'Function') {
    const fn = `${item.name}(${item.args.map(renderExpr).join(', ')})`;
    return item.alias ? `${fn} AS ${item.alias}` : fn;
  }
  // Star
  return item.table ? `${item.table}.*` : '*';
}

function renderExpr(expr: ExprNode): string {
  switch (expr.kind) {
    case 'Literal':
      if (expr.value === null) return 'NULL';
      if (typeof expr.value === 'string') return `'${expr.value.replace(/'/g, "''")}'`;
      return String(expr.value);
    case 'Column':
      return `${expr.table}.${expr.column}`;
    case 'Function':
      return `${expr.name}(${expr.args.map(renderExpr).join(', ')})`;
    case 'Cmp': {
      const lhs = renderExpr(expr.lhs);
      if (expr.op === 'IS NULL')     return `${lhs} IS NULL`;
      if (expr.op === 'IS NOT NULL') return `${lhs} IS NOT NULL`;
      const rhs = expr.rhs ? renderExpr(expr.rhs) : '';
      return `${lhs} ${expr.op} ${rhs}`;
    }
    case 'And':
      return `(${expr.args.map(renderExpr).join(' AND ')})`;
    case 'Or':
      return `(${expr.args.map(renderExpr).join(' OR ')})`;
    case 'Not':
      return `NOT (${renderExpr(expr.arg)})`;
    case 'Between':
      return `${renderExpr(expr.value)} BETWEEN ${renderExpr(expr.low)} AND ${renderExpr(expr.high)}`;
    default:
      return '?';
  }
}

// ─── Validator ────────────────────────────────────────────────────────────────

export function pepAstValidatorNode(
  state: PepGraphState,
  functionAllowlist?: Set<string>,
): Partial<PepGraphState> {
  const start = Date.now();

  if (!state.ast) {
    return span(state, start, { error: 'No AST to validate', abort_reason: 'validator_no_ast' });
  }
  if (!state.allowed_snapshot) {
    return span(state, start, { error: 'No snapshot for validation', abort_reason: 'validator_no_snapshot' });
  }

  const allowlist = functionAllowlist ?? DEFAULT_FUNCTION_ALLOWLIST;
  const errors: ValidationError[] = [];
  const ast = state.ast;
  const snapshot = state.allowed_snapshot;

  // Build fast-lookup maps from snapshot
  const tableMap = new Map(
    snapshot.tables.map((t) => [`${t.schema}.${t.name}`, t])
  );
  const columnMap = new Map(
    snapshot.tables.flatMap((t) =>
      t.columns.map((c) => [`${t.schema}.${t.name}.${c.name}`, c])
    )
  );
  // FK edge set: "tableA.colA -> tableB.colB"
  const fkEdges = new Set(
    snapshot.tables.flatMap((t) =>
      (t.foreign_keys ?? []).map(
        (fk) => `${t.schema}.${t.name}.${fk.column}:${fk.ref_table}.${fk.ref_column}`
      )
    )
  );

  // 1. Root table must be in snapshot
  const fromKey = `${ast.from.schema}.${ast.from.name}`;
  if (!tableMap.has(fromKey)) {
    errors.push({ code: 'table_not_permitted', message: `Table '${fromKey}' is not accessible`, hint: 'Only tables in your schema snapshot are accessible' });
  }

  // 2. Collect all referenced tables (from + joins)
  const referencedTables = new Set([fromKey]);
  for (const j of ast.joins ?? []) {
    const jk = `${j.table.schema}.${j.table.name}`;
    referencedTables.add(jk);
    if (!tableMap.has(jk)) {
      errors.push({ code: 'join_table_not_permitted', message: `JOIN table '${jk}' is not accessible`, node_path: 'joins' });
    }
  }

  // 3. Validate JOINs use FK edges only
  for (const j of ast.joins ?? []) {
    if (!isFkJoin(j.on, fkEdges, ast.from, j.table)) {
      errors.push({
        code: 'join_not_via_fk',
        message: `JOIN on '${ast.from.schema}.${ast.from.name}' ↔ '${j.table.schema}.${j.table.name}' does not follow a declared foreign key`,
        hint: 'JOINs must use columns declared as foreign keys in the schema',
        node_path: 'joins',
      });
    }
  }

  // 4. Validate SELECT items reference allowed columns
  for (const item of ast.select) {
    if (item.kind === 'Column') {
      validateColumn(item.table, item.column, tableMap, columnMap, errors, 'select');
    } else if (item.kind === 'Function') {
      validateFunction(item.name, allowlist, errors, 'select');
      collectExprColumns(item.args, tableMap, columnMap, allowlist, errors, 'select');
    }
  }

  // 5. Validate WHERE / GROUP BY / HAVING / ORDER BY recursively
  if (ast.where) {
    collectExprColumns([ast.where], tableMap, columnMap, allowlist, errors, 'where');
  }
  for (const g of ast.group_by ?? []) {
    collectExprColumns([g], tableMap, columnMap, allowlist, errors, 'group_by');
  }
  if (ast.having) {
    collectExprColumns([ast.having], tableMap, columnMap, allowlist, errors, 'having');
  }
  for (const o of ast.order_by ?? []) {
    collectExprColumns([o.expr], tableMap, columnMap, allowlist, errors, 'order_by');
  }

  // 6. LIMIT must be present (enforced by schema, but double-check)
  if (!ast.limit || ast.limit < 1) {
    errors.push({ code: 'limit_required', message: 'LIMIT is required on all queries', hint: 'Add a LIMIT clause' });
  }

  // 7. No subqueries, CTEs, window functions — enforced by the AST schema (they're not in the union).
  //    For belt+suspenders, verify no Function node with window-function names.
  const WINDOW_FNS = new Set(['row_number', 'rank', 'dense_rank', 'lead', 'lag', 'ntile', 'first_value', 'last_value']);
  walkExprFunctions(ast, (name) => {
    if (WINDOW_FNS.has(name.toLowerCase())) {
      errors.push({ code: 'window_function_denied', message: `Window function '${name}' is not permitted in V1`, hint: 'Use aggregate functions instead' });
    }
  });

  if (errors.length > 0) {
    for (const e of errors) {
      pepAstValidationFailuresTotal.inc({ reason: e.code });
    }
    return span(state, start, {
      validation_errors: errors,
      retry_count: (state.retry_count ?? 0) + 1,
    });
  }

  // Render to SQL only after passing all checks
  const validatedSql = renderAstToSql(ast);

  return span(state, start, {
    validated_sql: validatedSql,
    validation_errors: [],
  });
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

function validateColumn(
  tableName: string,
  columnName: string,
  tableMap: Map<string, unknown>,
  columnMap: Map<string, unknown>,
  errors: ValidationError[],
  path: string,
): void {
  // Try to resolve table alias to full qualified name
  const fullKey = [...tableMap.keys()].find((k) => k.endsWith(`.${tableName}`) || k === tableName);
  if (!fullKey) {
    errors.push({ code: 'table_not_permitted', message: `Table '${tableName}' is not in your schema snapshot`, node_path: path });
    return;
  }
  const colKey = `${fullKey}.${columnName}`;
  if (!columnMap.has(colKey)) {
    errors.push({
      code: 'column_not_permitted',
      message: `Column '${tableName}.${columnName}' is not accessible`,
      hint: `Check available columns for ${tableName}`,
      node_path: path,
    });
  }
}

function validateFunction(name: string, allowlist: Set<string>, errors: ValidationError[], path: string): void {
  if (!allowlist.has(name.toLowerCase())) {
    errors.push({
      code: 'function_not_allowed',
      message: `Function '${name}' is not in the permitted function allowlist`,
      hint: `Allowed functions: ${[...allowlist].join(', ')}`,
      node_path: path,
    });
  }
}

function collectExprColumns(
  exprs: ExprNode[],
  tableMap: Map<string, unknown>,
  columnMap: Map<string, unknown>,
  allowlist: Set<string>,
  errors: ValidationError[],
  path: string,
): void {
  for (const expr of exprs) {
    switch (expr.kind) {
      case 'Column':
        validateColumn(expr.table, expr.column, tableMap, columnMap, errors, path);
        break;
      case 'Function':
        validateFunction(expr.name, allowlist, errors, path);
        collectExprColumns(expr.args, tableMap, columnMap, allowlist, errors, path);
        break;
      case 'Cmp':
        collectExprColumns([expr.lhs], tableMap, columnMap, allowlist, errors, path);
        if (expr.rhs) collectExprColumns([expr.rhs], tableMap, columnMap, allowlist, errors, path);
        break;
      case 'And':
      case 'Or':
        collectExprColumns(expr.args, tableMap, columnMap, allowlist, errors, path);
        break;
      case 'Not':
        collectExprColumns([expr.arg], tableMap, columnMap, allowlist, errors, path);
        break;
      case 'Between':
        collectExprColumns([expr.value, expr.low, expr.high], tableMap, columnMap, allowlist, errors, path);
        break;
    }
  }
}

function isFkJoin(
  onExpr: ExprNode,
  fkEdges: Set<string>,
  fromTable: { schema: string; name: string },
  joinTable: { schema: string; name: string },
): boolean {
  // Accepts: col1 = col2 where one side is FK of the other
  if (onExpr.kind !== 'Cmp' || onExpr.op !== '=') return false;
  if (onExpr.lhs.kind !== 'Column' || !onExpr.rhs || onExpr.rhs.kind !== 'Column') return false;
  const l = onExpr.lhs;
  const r = onExpr.rhs;
  const fromFull = `${fromTable.schema}.${fromTable.name}`;
  const joinFull = `${joinTable.schema}.${joinTable.name}`;
  const edge1 = `${fromFull}.${l.column}:${joinFull}.${r.column}`;
  const edge2 = `${joinFull}.${r.column}:${fromFull}.${l.column}`;
  return fkEdges.has(edge1) || fkEdges.has(edge2);
}

function walkExprFunctions(ast: SqlAst, cb: (name: string) => void): void {
  function walk(expr: ExprNode): void {
    if (expr.kind === 'Function') {
      cb(expr.name);
      expr.args.forEach(walk);
    } else if (expr.kind === 'Cmp') {
      walk(expr.lhs);
      if (expr.rhs) walk(expr.rhs);
    } else if (expr.kind === 'And' || expr.kind === 'Or') {
      expr.args.forEach(walk);
    } else if (expr.kind === 'Not') {
      walk(expr.arg);
    } else if (expr.kind === 'Between') {
      [expr.value, expr.low, expr.high].forEach(walk);
    }
  }
  if (ast.where) walk(ast.where);
  for (const o of ast.order_by ?? []) walk(o.expr);
  for (const g of ast.group_by ?? []) walk(g);
  if (ast.having) walk(ast.having);
}

function span(state: PepGraphState, start: number, patch: Partial<PepGraphState>): Partial<PepGraphState> {
  return {
    ...patch,
    node_spans: [
      ...(state.node_spans ?? []),
      { node: 'pep_ast_validator', latency_ms: Date.now() - start, error: patch.error },
    ],
  };
}
