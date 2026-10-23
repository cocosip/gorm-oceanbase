package oceanbase

import (
	"database/sql"
	"fmt"
	"reflect"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/callbacks"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/schema"
)

// registerOracleCallbacks installs the Oracle compatibility mode callback set.
// It keeps the default transaction, model hook and association callbacks and
// replaces gorm:create with the Oracle builder that constructs and executes
// whole INSERT / INSERT ALL statements (design.md §5.3).
func registerOracleCallbacks(db *gorm.DB) {
	callbacks.RegisterDefaultCallbacks(db, &callbacks.Config{})
	// gorm:create is always present right after RegisterDefaultCallbacks.
	_ = db.Callback().Create().Replace("gorm:create", createOracle)
}

// oracleDefaultMarker is the DefaultValueOf marker for Oracle mode. The Oracle
// create callback consumes it by omitting the column; a row whose every column
// is a marker renders the single-column DEFAULT form.
type oracleDefaultMarker struct{}

func (oracleDefaultMarker) Build(builder clause.Builder) {
	writeString(builder, "DEFAULT")
}

// oracleInsertGroup is one executable statement's worth of rows. Groups only
// merge adjacent rows with the identical effective column set; sourceRows
// keeps the origin row of every entry for upsert and primary-key contexts.
type oracleInsertGroup struct {
	columns    []clause.Column
	rows       [][]interface{}
	sourceRows []int
}

// createOracle replaces gorm:create for Oracle compatibility mode. It reads
// the VALUES clause context, validates the whole batch, then builds and
// executes one INSERT or INSERT ALL per adjacent same-column group, or one
// MERGE per row when an ON CONFLICT clause is present. Missing database-default
// fields are omitted from the column list; explicit NULL is preserved as a
// bound NULL (design.md §5.3).
func createOracle(db *gorm.DB) {
	if db.Error != nil {
		return
	}

	stmt := db.Statement
	if stmt.Schema != nil && !stmt.Unscoped {
		for _, c := range stmt.Schema.CreateClauses {
			stmt.AddClause(c)
		}
	}

	var built []oracleInsertSQL
	switch {
	case stmt.SQL.Len() != 0:
		// A plugin or caller already built the statement; execute it as-is.
		built = []oracleInsertSQL{{sql: stmt.SQL.String(), vars: stmt.Vars}}
	case hasOracleOnConflict(stmt):
		built = buildOracleUpserts(db)
	default:
		built = buildOraclePlainInserts(db)
	}
	execOracleInserts(db, built)
}

func hasOracleOnConflict(stmt *gorm.Statement) bool {
	_, ok := stmt.Clauses["ON CONFLICT"]
	return ok
}

// buildOraclePlainInserts converts the create values into INSERT / INSERT ALL
// statements for the batches without an upsert clause. Rows omitting the
// single auto-generated primary key run as INSERT RETURNING statements, one
// per row, so the generated keys are fetched and written back (design.md §6).
func buildOraclePlainInserts(db *gorm.DB) []oracleInsertSQL {
	stmt := db.Statement
	values := callbacks.ConvertToCreateValues(stmt)
	if db.Error != nil {
		return nil
	}
	groups := groupOracleInsertRows(values)
	if len(groups) == 0 {
		// Every column of the record falls back to a database default; render
		// the single-column DEFAULT form instead of nothing.
		if field := firstOracleDefaultField(stmt); field != nil {
			groups = []oracleInsertGroup{{
				columns:    []clause.Column{{Name: field.DBName}},
				rows:       [][]interface{}{{oracleDefaultMarker{}}},
				sourceRows: []int{0},
			}}
		} else {
			_ = db.AddError(gorm.ErrInvalidData)
			return nil
		}
	}

	pkField := oracleGeneratedPKField(stmt)
	ins := oracleInsertClause(stmt)
	built := make([]oracleInsertSQL, 0, len(groups))
	for _, g := range groups {
		if pkField != nil && !oracleRowHasColumn(g.columns, pkField.DBName) {
			// Generated keys are fetched row by row; never batched.
			for i, rowValues := range g.rows {
				built = append(built, buildOracleInsertReturning(stmt, ins, pkField, g.columns, rowValues, g.sourceRows[i]))
			}
			continue
		}
		stmt.SQL = strings.Builder{}
		stmt.Vars = make([]interface{}, 0, len(g.columns)*len(g.rows))
		writeOracleInsert(stmt, ins, g)
		built = append(built, oracleInsertSQL{
			sql:  stmt.SQL.String(),
			vars: append([]interface{}(nil), stmt.Vars...),
		})
	}
	return built
}

// oracleInsertClause reads an explicit INSERT clause, if the caller set one.
func oracleInsertClause(stmt *gorm.Statement) clause.Insert {
	if c, ok := stmt.Clauses["INSERT"]; ok {
		if ins, ok := c.Expression.(clause.Insert); ok {
			return ins
		}
	}
	return clause.Insert{}
}

// oracleGeneratedPKField returns the single auto-generated, readable integer
// primary key of the schema, or nil when the model does not have exactly one.
func oracleGeneratedPKField(stmt *gorm.Statement) *schema.Field {
	if stmt.Schema == nil || len(stmt.Schema.PrimaryFields) != 1 {
		return nil
	}
	pk := stmt.Schema.PrioritizedPrimaryField
	if pk == nil || !pk.HasDefaultValue || !pk.Readable {
		return nil
	}
	switch pk.DataType {
	case schema.Int, schema.Uint:
		return pk
	default:
		return nil
	}
}

// oraclePKBackfill carries the out-parameter binding of one INSERT RETURNING
// statement back to the model field it fills.
type oraclePKBackfill struct {
	field     *schema.Field
	sourceRow int
	dest      interface{}
}

// oracleUpsertRow is one row of an upsert batch with its effective column set
// and the primary-key branching decision.
type oracleUpsertRow struct {
	columns   []clause.Column
	values    []interface{}
	generate  bool
	sourceRow int
}

// buildOracleUpserts validates the whole upsert batch, then builds one MERGE
// statement per row. Rows whose only conflict target is their single missing
// auto-generated primary key are flagged for the INSERT RETURNING path, which
// is delivered by the Oracle primary key task (design.md §5.3, R14).
func buildOracleUpserts(db *gorm.DB) []oracleInsertSQL {
	stmt := db.Statement
	// ConvertToCreateValues also expands UpdateAll into DoUpdates assignments.
	values := callbacks.ConvertToCreateValues(stmt)
	if db.Error != nil {
		return nil
	}
	c, ok := stmt.Clauses["ON CONFLICT"]
	oc, isOC := c.Expression.(clause.OnConflict)
	if !ok || !isOC {
		_ = db.AddError(gorm.ErrInvalidData)
		return nil
	}

	target, err := oracleConflictTarget(stmt, oc)
	if err != nil {
		_ = db.AddError(err)
		return nil
	}
	if err := validateOracleOnConflict(oc, target); err != nil {
		_ = db.AddError(err)
		return nil
	}

	rows := make([]oracleUpsertRow, 0, len(values.Values))
	for rowIdx, row := range values.Values {
		upsertRow := oracleUpsertRow{sourceRow: rowIdx}
		for idx, v := range row {
			if _, isMarker := v.(oracleDefaultMarker); isMarker {
				continue
			}
			upsertRow.columns = append(upsertRow.columns, values.Columns[idx])
			upsertRow.values = append(upsertRow.values, v)
		}
		if len(upsertRow.columns) == 0 {
			_ = db.AddError(fmt.Errorf("%w: upsert row without columns", gorm.ErrInvalidData))
			return nil
		}
		upsertRow.generate = oracleRowGeneratesPK(stmt, target, upsertRow.columns)
		rows = append(rows, upsertRow)
	}

	// Every row is validated before the batch executes any DML.
	for i := range rows {
		if rows[i].generate {
			continue
		}
		for _, tc := range target {
			if !oracleRowHasColumn(rows[i].columns, tc.Name) {
				_ = db.AddError(fmt.Errorf("%w: conflict target column %q is not provided by every row", ErrUpsertUnsupported, tc.Name))
				return nil
			}
		}
	}
	// Explicit assignments reading a column the row does not provide cannot
	// apply; refusing beats silently dropping the semantics (design.md §5.3.3).
	// UpdateAll assignments are auto-expanded over the batch's union columns,
	// so per-row skipping stays valid for them.
	if !oc.UpdateAll {
		for i := range rows {
			if rows[i].generate {
				continue
			}
			for _, assignment := range oc.DoUpdates {
				if col, ok := assignment.Value.(clause.Column); ok && col.Table == excludedTable &&
					!oracleRowHasColumn(rows[i].columns, col.Name) {
					_ = db.AddError(fmt.Errorf("%w: assignment column %q is not provided by every row", ErrUpsertUnsupported, col.Name))
					return nil
				}
			}
		}
	}

	pkField := oracleGeneratedPKField(stmt)
	ins := oracleInsertClause(stmt)
	built := make([]oracleInsertSQL, 0, len(rows))
	for _, row := range rows {
		if row.generate {
			// New records whose only conflict target is the missing generated
			// key: INSERT RETURNING fills the key before associations write
			// their foreign keys (design.md §5.3.4).
			built = append(built, buildOracleInsertReturning(stmt, ins, pkField, row.columns, row.values, row.sourceRow))
			continue
		}
		stmt.SQL = strings.Builder{}
		stmt.Vars = make([]interface{}, 0, 2*len(row.columns))
		writeOracleMerge(stmt, ins, oc, target, row)
		built = append(built, oracleInsertSQL{
			sql:  stmt.SQL.String(),
			vars: append([]interface{}(nil), stmt.Vars...),
		})
	}
	return built
}

// oracleConflictTarget resolves the upsert match columns: explicit ON CONFLICT
// columns, or the model's complete primary key.
func oracleConflictTarget(stmt *gorm.Statement, oc clause.OnConflict) ([]clause.Column, error) {
	if len(oc.Columns) > 0 {
		target := make([]clause.Column, 0, len(oc.Columns))
		for _, c := range oc.Columns {
			if c.Name == "" {
				return nil, fmt.Errorf("%w: empty conflict target column", ErrUpsertUnsupported)
			}
			target = append(target, clause.Column{Name: c.Name})
		}
		return target, nil
	}
	if stmt.Schema == nil || len(stmt.Schema.PrimaryFields) == 0 {
		return nil, fmt.Errorf("%w: no conflict target columns and the model has no primary key", ErrUpsertUnsupported)
	}
	target := make([]clause.Column, 0, len(stmt.Schema.PrimaryFields))
	for _, field := range stmt.Schema.PrimaryFields {
		target = append(target, clause.Column{Name: field.DBName})
	}
	return target, nil
}

// validateOracleOnConflict rejects the upsert shapes the MERGE path cannot
// serve in this delivery. Assignments touching the conflict target are
// refused instead of silently dropped.
func validateOracleOnConflict(oc clause.OnConflict, target []clause.Column) error {
	if oc.OnConstraint != "" {
		return fmt.Errorf("%w: ON CONSTRAINT targets are not supported by Oracle MERGE", ErrUpsertUnsupported)
	}
	if len(oc.TargetWhere.Exprs) > 0 {
		return fmt.Errorf("%w: conditional conflict targets are not supported", ErrUpsertUnsupported)
	}
	if len(oc.Where.Exprs) > 0 {
		return fmt.Errorf("%w: conditional upserts (WHERE on DO UPDATE) are not supported", ErrUpsertUnsupported)
	}
	for _, assignment := range oc.DoUpdates {
		for _, tc := range target {
			if assignment.Column.Name == tc.Name {
				return fmt.Errorf("%w: updating conflict-target column %q is not supported", ErrUpsertUnsupported, tc.Name)
			}
		}
	}
	return nil
}

// oracleRowGeneratesPK reports whether the row must take the generated primary
// key path: the conflict target is exactly the single auto-generated primary
// key and the row leaves that key at its default (design.md §5.3.4).
func oracleRowGeneratesPK(stmt *gorm.Statement, target []clause.Column, cols []clause.Column) bool {
	if len(target) != 1 {
		return false
	}
	pk := oracleGeneratedPKField(stmt)
	if pk == nil || pk.DBName != target[0].Name {
		return false
	}
	return !oracleRowHasColumn(cols, pk.DBName)
}

func oracleRowHasColumn(cols []clause.Column, name string) bool {
	for _, c := range cols {
		if c.Name == name {
			return true
		}
	}
	return false
}

// writeOracleMerge renders one row as a MERGE statement with a source select
// over DUAL. The update branch is omitted for DoNothing and for rows whose
// assignments resolve to nothing.
func writeOracleMerge(stmt *gorm.Statement, ins clause.Insert, oc clause.OnConflict, target []clause.Column, row oracleUpsertRow) {
	table := ins.Table
	if table.Name == "" {
		table = clause.Table{Name: clause.CurrentTable}
	}

	writeString(stmt, "MERGE INTO ")
	stmt.WriteQuoted(table)
	writeString(stmt, " USING (SELECT ")
	for i, value := range row.values {
		if i > 0 {
			writeString(stmt, ", ")
		}
		stmt.AddVar(stmt, value)
		writeByte(stmt, ' ')
		stmt.WriteQuoted(row.columns[i])
	}
	writeString(stmt, " FROM DUAL) S ON (")
	for i, tc := range target {
		if i > 0 {
			writeString(stmt, " AND ")
		}
		stmt.WriteQuoted(clause.Column{Table: "s", Name: tc.Name})
		writeString(stmt, " = ")
		stmt.WriteQuoted(clause.Column{Table: table.Name, Name: tc.Name})
	}
	writeByte(stmt, ')')

	if assignments := oracleUpsertAssignments(oc, row); len(assignments) > 0 {
		writeString(stmt, " WHEN MATCHED THEN UPDATE SET ")
		for i, assignment := range assignments {
			if i > 0 {
				writeString(stmt, ", ")
			}
			stmt.WriteQuoted(clause.Column{Table: table.Name, Name: assignment.Column.Name})
			writeString(stmt, " = ")
			if col, ok := assignment.Value.(clause.Column); ok && col.Table == excludedTable {
				stmt.WriteQuoted(clause.Column{Table: "s", Name: col.Name})
			} else {
				stmt.AddVar(stmt, assignment.Value)
			}
		}
	}

	writeString(stmt, " WHEN NOT MATCHED THEN INSERT (")
	for i, col := range row.columns {
		if i > 0 {
			writeByte(stmt, ',')
		}
		stmt.WriteQuoted(clause.Column{Name: col.Name})
	}
	writeString(stmt, ") VALUES (")
	for i, col := range row.columns {
		if i > 0 {
			writeString(stmt, ", ")
		}
		stmt.WriteQuoted(clause.Column{Table: "s", Name: col.Name})
	}
	writeByte(stmt, ')')
}

// oracleUpsertAssignments resolves the update branch for one row. UpdateAll
// was already expanded into DoUpdates by ConvertToCreateValues over the
// batch's union columns, so only those auto-generated assignments may be
// skipped for rows providing a narrower column set; explicit assignments
// missing a column were rejected before any DML.
func oracleUpsertAssignments(oc clause.OnConflict, row oracleUpsertRow) clause.Set {
	if oc.DoNothing {
		return nil
	}
	var out clause.Set
	for _, assignment := range oc.DoUpdates {
		if col, ok := assignment.Value.(clause.Column); ok && col.Table == excludedTable {
			if !oracleRowHasColumn(row.columns, col.Name) {
				continue
			}
		}
		out = append(out, assignment)
	}
	return out
}

// execOracleInserts joins the built statements for a dry run, or executes them
// in order, applies the primary key backfills and accumulates the affected row
// count. A failed statement is never replayed.
func execOracleInserts(db *gorm.DB, built []oracleInsertSQL) {
	stmt := db.Statement
	if db.Error != nil || len(built) == 0 {
		return
	}

	if db.DryRun {
		stmt.SQL = strings.Builder{}
		mergedVars := make([]interface{}, 0, len(built)*2)
		for i, b := range built {
			if i > 0 {
				stmt.SQL.WriteString(";\n")
			}
			stmt.SQL.WriteString(b.sql)
			mergedVars = append(mergedVars, b.vars...)
		}
		stmt.Vars = mergedVars
		return
	}

	var (
		rowsAffected int64
		lastResult   sql.Result
	)
	for _, b := range built {
		result, err := stmt.ConnPool.ExecContext(stmt.Context, b.sql, b.vars...)
		if err != nil {
			_ = db.AddError(err)
			break
		}
		rowsAffected += mustRowsAffected(result)
		lastResult = result
		if b.backfill != nil {
			if err := oracleApplyBackfill(stmt, b.backfill); err != nil {
				_ = db.AddError(err)
				break
			}
		}
	}
	db.RowsAffected = rowsAffected
	if db.Statement.Result != nil {
		db.Statement.Result.Result = lastResult
		db.Statement.Result.RowsAffected = rowsAffected
	}
}

// oracleApplyBackfill copies the fetched generated key into the model field of
// the statement's source row. Map destinations follow GORM's convention of
// writing the key back under the column name.
func oracleApplyBackfill(stmt *gorm.Statement, backfill *oraclePKBackfill) error {
	var value interface{}
	switch dest := backfill.dest.(type) {
	case *uint64:
		value = *dest
	case *int64:
		value = *dest
	default:
		return fmt.Errorf("oceanbase: unsupported generated key destination %T", backfill.dest)
	}

	switch values := stmt.Dest.(type) {
	case map[string]interface{}:
		values[backfill.field.DBName] = value
		return nil
	case *map[string]interface{}:
		(*values)[backfill.field.DBName] = value
		return nil
	case []map[string]interface{}:
		return oracleSetMapRowKey(values, backfill, value)
	case *[]map[string]interface{}:
		if values == nil {
			return fmt.Errorf("oceanbase: generated key row %d out of range", backfill.sourceRow)
		}
		return oracleSetMapRowKey(*values, backfill, value)
	}

	rv := stmt.ReflectValue
	switch rv.Kind() {
	case reflect.Slice, reflect.Array:
		if backfill.sourceRow >= rv.Len() {
			return fmt.Errorf("oceanbase: generated key row %d out of range", backfill.sourceRow)
		}
		rv = reflect.Indirect(rv.Index(backfill.sourceRow))
	}
	return backfill.field.Set(stmt.Context, rv, value)
}

func oracleSetMapRowKey(values []map[string]interface{}, backfill *oraclePKBackfill, value interface{}) error {
	if backfill.sourceRow >= len(values) || values[backfill.sourceRow] == nil {
		return fmt.Errorf("oceanbase: generated key row %d out of range", backfill.sourceRow)
	}
	values[backfill.sourceRow][backfill.field.DBName] = value
	return nil
}

// oracleInsertSQL is one fully built INSERT statement with its own vars.
type oracleInsertSQL struct {
	sql      string
	vars     []interface{}
	backfill *oraclePKBackfill
}

// buildOracleInsertReturning renders one row as
// INSERT INTO ... VALUES (...) RETURNING <pk> INTO ? and prepares the out
// parameter the driver fills with the generated key. The statement is never
// replayed and never falls back to LastInsertId (design.md §6.1).
func buildOracleInsertReturning(stmt *gorm.Statement, ins clause.Insert, pkField *schema.Field, cols []clause.Column, values []interface{}, sourceRow int) oracleInsertSQL {
	dest := oraclePKDest(pkField)
	out := sql.Out{Dest: dest}

	stmt.SQL = strings.Builder{}
	stmt.Vars = make([]interface{}, 0, len(cols)+1)
	writeOracleInsertSingle(stmt, ins, cols, values)
	writeString(stmt, " RETURNING ")
	stmt.WriteQuoted(clause.Column{Name: pkField.DBName})
	writeString(stmt, " INTO ")
	stmt.AddVar(stmt, out)

	return oracleInsertSQL{
		sql:  stmt.SQL.String(),
		vars: append([]interface{}(nil), stmt.Vars...),
		backfill: &oraclePKBackfill{
			field:     pkField,
			sourceRow: sourceRow,
			dest:      dest,
		},
	}
}

// oraclePKDest allocates the out destination matching the primary key kind.
func oraclePKDest(pk *schema.Field) interface{} {
	if pk.DataType == schema.Uint {
		return new(uint64)
	}
	return new(int64)
}

// groupOracleInsertRows splits converted create values into adjacent groups
// with the identical effective column set. Database-default markers are
// consumed here: their columns are omitted, unless every column of the row is
// a marker, in which case the first column renders as DEFAULT.
func groupOracleInsertRows(values clause.Values) []oracleInsertGroup {
	if len(values.Columns) == 0 || len(values.Values) == 0 {
		return nil
	}

	var (
		groups []oracleInsertGroup
		curSig []int
	)
	for rowIdx, row := range values.Values {
		sig := make([]int, 0, len(row))
		for idx, v := range row {
			if _, isMarker := v.(oracleDefaultMarker); !isMarker {
				sig = append(sig, idx)
			}
		}
		if len(sig) == 0 {
			sig = append(sig, 0)
		}

		if !equalIndexes(curSig, sig) {
			curSig = sig
			columns := make([]clause.Column, 0, len(sig))
			for _, idx := range sig {
				columns = append(columns, values.Columns[idx])
			}
			groups = append(groups, oracleInsertGroup{columns: columns})
		}

		g := &groups[len(groups)-1]
		vals := make([]interface{}, 0, len(sig))
		for _, idx := range sig {
			vals = append(vals, row[idx])
		}
		g.rows = append(g.rows, vals)
		g.sourceRows = append(g.sourceRows, rowIdx)
	}
	return groups
}

// writeOracleInsert renders the group as one INSERT (single row) or INSERT ALL
// statement. Expression values (including leftover markers) build themselves;
// everything else is bound as a positional parameter.
func writeOracleInsert(stmt *gorm.Statement, ins clause.Insert, g oracleInsertGroup) {
	if len(g.rows) == 1 {
		writeOracleInsertSingle(stmt, ins, g.columns, g.rows[0])
		return
	}

	writeString(stmt, "INSERT ALL")
	for _, row := range g.rows {
		// Oracle's INSERT ALL carries no statement modifier; ins.Modifier is
		// intentionally not rendered on this branch.
		writeString(stmt, " INTO ")
		stmt.WriteQuoted(oracleInsertTable(ins))
		writeOracleValues(stmt, g.columns, row)
	}
	writeString(stmt, " SELECT 1 FROM DUAL")
}

// writeOracleInsertSingle renders INSERT INTO <table> for one row; callers
// append RETURNING or finish the statement.
func writeOracleInsertSingle(stmt *gorm.Statement, ins clause.Insert, cols []clause.Column, values []interface{}) {
	if ins.Modifier != "" {
		writeString(stmt, ins.Modifier)
		writeByte(stmt, ' ')
	}
	writeString(stmt, "INSERT INTO ")
	stmt.WriteQuoted(oracleInsertTable(ins))
	writeOracleValues(stmt, cols, values)
}

// oracleInsertTable resolves the INSERT target table.
func oracleInsertTable(ins clause.Insert) clause.Table {
	if ins.Table.Name != "" {
		return ins.Table
	}
	return clause.Table{Name: clause.CurrentTable}
}

func writeOracleValues(stmt *gorm.Statement, columns []clause.Column, row []interface{}) {
	writeByte(stmt, ' ')
	writeByte(stmt, '(')
	for idx, column := range columns {
		if idx > 0 {
			writeByte(stmt, ',')
		}
		stmt.WriteQuoted(column)
	}
	writeByte(stmt, ')')
	writeString(stmt, " VALUES (")
	stmt.AddVar(stmt, row...)
	writeByte(stmt, ')')
}

// firstOracleDefaultField returns the first database-default field of the
// schema for the all-default single-column DEFAULT insert.
func firstOracleDefaultField(stmt *gorm.Statement) *schema.Field {
	for _, field := range stmt.Schema.FieldsWithDefaultDBValue {
		if field.DBName != "" {
			return field
		}
	}
	return nil
}

// mustRowsAffected extracts the affected row count. GORM's writers and the
// driver always provide a result here; a failure falls back to zero.
func mustRowsAffected(result sql.Result) int64 {
	n, err := result.RowsAffected()
	if err != nil {
		return 0
	}
	return n
}

func equalIndexes(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// oracleClauseBuilders returns the Oracle clause rewrites.
func oracleClauseBuilders(_ *Dialector) map[string]clause.ClauseBuilder {
	return map[string]clause.ClauseBuilder{
		clauseLimit: buildFetchFirst,
		clauseFor:   buildOracleLocking,
	}
}

// buildFetchFirst renders GORM's LIMIT clause as Oracle row-limiting syntax:
// OFFSET n ROWS FETCH FIRST m ROWS ONLY.
func buildFetchFirst(c clause.Clause, builder clause.Builder) {
	limit, ok := c.Expression.(clause.Limit)
	if !ok {
		c.Build(builder)
		return
	}

	if limit.Offset > 0 {
		writeString(builder, "OFFSET ")
		builder.AddVar(builder, limit.Offset)
		writeString(builder, " ROWS")
	}
	if limit.Limit != nil && *limit.Limit >= 0 {
		if limit.Offset > 0 {
			writeByte(builder, ' ')
		}
		writeString(builder, "FETCH FIRST ")
		builder.AddVar(builder, *limit.Limit)
		writeString(builder, " ROWS ONLY")
	}
}

// buildOracleLocking degrades FOR SHARE to FOR UPDATE. Oracle compatibility
// mode has no shared-lock read syntax, so the strength is rewritten and the
// semantic change is documented in the README (design.md §5); every other
// locking form builds unchanged.
func buildOracleLocking(c clause.Clause, builder clause.Builder) {
	if locking, ok := c.Expression.(clause.Locking); ok && strings.EqualFold(locking.Strength, "SHARE") {
		locking.Strength = "UPDATE"
		c.Expression = locking
	}
	c.Build(builder)
}

// oracleQuoteTo quotes identifiers with double quotes. OceanBase's Oracle mode
// folds unquoted identifiers to upper case, so every identifier part that does
// not carry its own quotes is written upper case; a part that arrives
// self-quoted (an explicit-case name) is preserved verbatim, and embedded
// quotes are escaped by doubling. The state machine mirrors the MySQL one.
func oracleQuoteTo(writer clause.Writer, str string) {
	var (
		underQuoted, selfQuoted bool
		continuousQuote         int8
		shiftDelimiter          int8
	)

	for _, v := range []byte(str) {
		switch v {
		case '"':
			continuousQuote++
			if continuousQuote == 2 {
				writeString(writer, `""`)
				continuousQuote = 0
			}
		case '.':
			if continuousQuote > 0 || !selfQuoted {
				shiftDelimiter = 0
				underQuoted = false
				continuousQuote = 0
				writeByte(writer, '"')
			}
			writeByte(writer, v)
			continue
		default:
			if shiftDelimiter-continuousQuote <= 0 && !underQuoted {
				writeByte(writer, '"')
				underQuoted = true
				if selfQuoted = continuousQuote > 0; selfQuoted {
					continuousQuote--
				}
			}

			for ; continuousQuote > 0; continuousQuote-- {
				writeString(writer, `""`)
			}

			if selfQuoted {
				writeByte(writer, v)
			} else {
				writeByte(writer, asciiUpper(v))
			}
		}
		shiftDelimiter++
	}

	if continuousQuote > 0 && !selfQuoted {
		writeString(writer, `""`)
	}
	writeByte(writer, '"')
}

// asciiUpper uppercases one ASCII letter byte. UTF-8 continuation bytes stay
// untouched so multi-byte identifiers are not corrupted.
func asciiUpper(v byte) byte {
	if v >= 'a' && v <= 'z' {
		return v - ('a' - 'A')
	}
	return v
}

// Oracle column type limits. Extended (32k) VARCHAR2 and RAW sizes depend on
// server settings, so the standard limits decide the LOB switch.
const (
	// oracleVarchar2Max is the standard VARCHAR2 byte limit.
	oracleVarchar2Max = 4000
	// oracleRawMax is the standard RAW byte limit.
	oracleRawMax = 2000
	// oracleDefaultStringSize is the fallback length for an unsized string.
	oracleDefaultStringSize = 512
)

// oracleDataTypeOf maps a schema field to its Oracle-compatible column type.
func oracleDataTypeOf(d *Dialector, field *schema.Field) string {
	switch field.DataType {
	case schema.Bool:
		return "NUMBER(1)"
	case schema.Int, schema.Uint:
		return oracleNumberType(field)
	case schema.Float:
		return d.oracleFloatType(field)
	case schema.String:
		return d.oracleStringType(field)
	case schema.Time:
		return d.oracleTimeType(field)
	case schema.Bytes:
		return d.oracleBytesType(field)
	default:
		// Complex types (types.Object/Array) and named server types cannot be
		// inferred from the Go type; they need an explicit gorm:"type:..." tag
		// and are passed through verbatim (design.md §5.2).
		return string(field.DataType)
	}
}

// oracleNumberType maps the field bit size to a NUMBER precision. int64 needs
// 19 digits, uint64 needs 20 (max 18446744073709551615).
func oracleNumberType(field *schema.Field) string {
	var precision int
	switch {
	case field.Size <= 8:
		precision = 3
	case field.Size <= 16:
		precision = 5
	case field.Size <= 32:
		precision = 10
	case field.DataType == schema.Uint:
		precision = 20
	default:
		precision = 19
	}
	return fmt.Sprintf("NUMBER(%d,0)", precision)
}

func (d *Dialector) oracleFloatType(field *schema.Field) string {
	if field.Precision > 0 {
		return fmt.Sprintf("NUMBER(%d,%d)", field.Precision, field.Scale)
	}
	if field.Size <= 32 {
		return "BINARY_FLOAT"
	}
	return "BINARY_DOUBLE"
}

func (d *Dialector) oracleStringType(field *schema.Field) string {
	size := field.Size
	if size == 0 {
		if d.DefaultStringSize > 0 {
			size = int(d.DefaultStringSize)
		} else {
			size = oracleDefaultStringSize
		}
	}
	if size > oracleVarchar2Max {
		return "CLOB"
	}
	return fmt.Sprintf("VARCHAR2(%d)", size)
}

// oracleTimeType renders TIMESTAMP with the field precision; the default
// matches the Oracle TIMESTAMP default of microseconds. TIME ZONE variants are
// chosen with an explicit type tag and need the driver's Scanner types when
// read back (design.md §5.2).
func (d *Dialector) oracleTimeType(field *schema.Field) string {
	if field.Precision == 0 && field.TagSettings["PRECISION"] == "" {
		field.Precision = oracleDatetimePrecision
	}
	if field.Precision > 0 {
		return fmt.Sprintf("TIMESTAMP(%d)", field.Precision)
	}
	return "TIMESTAMP"
}

func (d *Dialector) oracleBytesType(field *schema.Field) string {
	if field.Size > 0 && field.Size <= oracleRawMax {
		return fmt.Sprintf("RAW(%d)", field.Size)
	}
	return "BLOB"
}
