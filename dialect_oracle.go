package oceanbase

import (
	"database/sql"
	"fmt"
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
// executes one INSERT or INSERT ALL per adjacent same-column group. Missing
// database-default fields are omitted from the column list; explicit NULL is
// preserved as a bound NULL. Generated primary keys are handled by the Oracle
// RETURNING support task (design.md §5.3, §6).
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

	if _, ok := stmt.Clauses["ON CONFLICT"]; ok {
		_ = db.AddError(fmt.Errorf("%w: ON CONFLICT needs the Oracle MERGE path", ErrUpsertUnsupported))
		return
	}

	built := make([]oracleInsertSQL, 0, 2)
	if stmt.SQL.Len() != 0 {
		// A plugin or caller already built the statement; execute it as-is.
		built = append(built, oracleInsertSQL{sql: stmt.SQL.String(), vars: stmt.Vars})
	} else {
		values := callbacks.ConvertToCreateValues(stmt)
		if db.Error != nil {
			return
		}
		groups := groupOracleInsertRows(values)
		if len(groups) == 0 {
			// Every column of the record falls back to a database default;
			// render the single-column DEFAULT form instead of nothing.
			if field := firstOracleDefaultField(stmt); field != nil {
				groups = []oracleInsertGroup{{
					columns:    []clause.Column{{Name: field.DBName}},
					rows:       [][]interface{}{{oracleDefaultMarker{}}},
					sourceRows: []int{0},
				}}
			} else {
				_ = db.AddError(gorm.ErrInvalidData)
				return
			}
		}

		for _, g := range groups {
			ins := clause.Insert{}
			if c, ok := stmt.Clauses["INSERT"]; ok {
				if userIns, ok := c.Expression.(clause.Insert); ok {
					ins = userIns
				}
			}
			stmt.SQL = strings.Builder{}
			stmt.Vars = make([]interface{}, 0, len(g.columns)*len(g.rows))
			writeOracleInsert(stmt, ins, g)
			built = append(built, oracleInsertSQL{
				sql:  stmt.SQL.String(),
				vars: append([]interface{}(nil), stmt.Vars...),
			})
		}
	}

	if db.DryRun {
		stmt.SQL = strings.Builder{}
		mergedVars := make([]interface{}, 0, len(built))
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
	}
	db.RowsAffected = rowsAffected
	if db.Statement.Result != nil {
		db.Statement.Result.Result = lastResult
		db.Statement.Result.RowsAffected = rowsAffected
	}
}

// oracleInsertSQL is one fully built INSERT statement with its own vars.
type oracleInsertSQL struct {
	sql  string
	vars []interface{}
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
	table := ins.Table
	if table.Name == "" {
		table = clause.Table{Name: clause.CurrentTable}
	}

	if len(g.rows) == 1 {
		if ins.Modifier != "" {
			writeString(stmt, ins.Modifier)
			writeByte(stmt, ' ')
		}
		writeString(stmt, "INSERT INTO ")
		stmt.WriteQuoted(table)
		writeOracleValues(stmt, g.columns, g.rows[0])
		return
	}

	writeString(stmt, "INSERT ALL")
	for _, row := range g.rows {
		writeString(stmt, " INTO ")
		stmt.WriteQuoted(table)
		writeOracleValues(stmt, g.columns, row)
	}
	writeString(stmt, " SELECT 1 FROM DUAL")
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
