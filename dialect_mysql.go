package oceanbase

import (
	"fmt"
	"math"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/callbacks"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/schema"
)

// MySQL-mode clause names.
const (
	clauseOnConflict = "ON CONFLICT"
	clauseValues     = "VALUES"
	clauseFor        = "FOR"
	clauseLimit      = "LIMIT"
	clauseOrderBy    = "ORDER BY"
	clauseWhere      = "WHERE"

	// excludedTable is the GORM pseudo table of an upsert row's new values.
	excludedTable = "excluded"
)

// mysqlCreateClauses is the create clause set for MySQL compatibility mode.
var mysqlCreateClauses = []string{"INSERT", "VALUES", "ON CONFLICT"}

// registerMySQLCallbacks installs the default callback chain extended with
// MySQL-only LIMIT support on update and delete.
func registerMySQLCallbacks(db *gorm.DB) {
	callbacks.RegisterDefaultCallbacks(db, &callbacks.Config{
		CreateClauses: mysqlCreateClauses,
		QueryClauses:  []string{"SELECT", "FROM", clauseWhere, "GROUP BY", clauseOrderBy, clauseLimit, "FOR"},
		UpdateClauses: []string{"UPDATE", "SET", clauseWhere, clauseOrderBy, clauseLimit},
		DeleteClauses: []string{"DELETE", "FROM", clauseWhere, clauseOrderBy, clauseLimit},
	})
}

// mysqlClauseBuilders returns the MySQL clause rewrites.
func mysqlClauseBuilders(d *Dialector) map[string]clause.ClauseBuilder {
	builders := map[string]clause.ClauseBuilder{
		clauseOnConflict: buildOnDuplicateKeyUpdate,
		clauseValues:     buildEmptyValues,
	}
	if d.DontSupportForShareClause {
		builders[clauseFor] = buildLockInShareMode
	}
	return builders
}

// buildOnDuplicateKeyUpdate renders GORM's ON CONFLICT as MySQL's
// ON DUPLICATE KEY UPDATE, including the excluded -> VALUES(col) rewrite.
func buildOnDuplicateKeyUpdate(c clause.Clause, builder clause.Builder) {
	onConflict, ok := c.Expression.(clause.OnConflict)
	if !ok {
		c.Build(builder)
		return
	}

	writeString(builder, "ON DUPLICATE KEY UPDATE ")
	if len(onConflict.DoUpdates) == 0 {
		if stmt, ok := builder.(*gorm.Statement); ok && stmt.Schema != nil {
			var column clause.Column
			onConflict.DoNothing = false

			if stmt.Schema.PrioritizedPrimaryField != nil {
				column = clause.Column{Name: stmt.Schema.PrioritizedPrimaryField.DBName}
			} else if len(stmt.Schema.DBNames) > 0 {
				column = clause.Column{Name: stmt.Schema.DBNames[0]}
			}

			if column.Name != "" {
				onConflict.DoUpdates = []clause.Assignment{{Column: column, Value: column}}
			}
			stmt.AddClause(onConflict)
		}
	}

	for idx, assignment := range onConflict.DoUpdates {
		if idx > 0 {
			writeByte(builder, ',')
		}
		builder.WriteQuoted(assignment.Column)
		writeByte(builder, '=')
		if column, ok := assignment.Value.(clause.Column); ok && column.Table == excludedTable {
			column.Table = ""
			writeString(builder, "VALUES(")
			builder.WriteQuoted(column)
			writeByte(builder, ')')
		} else {
			builder.AddVar(builder, assignment.Value)
		}
	}
}

// buildEmptyValues renders the MySQL-specific empty VALUES() form.
func buildEmptyValues(c clause.Clause, builder clause.Builder) {
	if values, ok := c.Expression.(clause.Values); ok && len(values.Columns) == 0 {
		writeString(builder, "VALUES()")
		return
	}
	c.Build(builder)
}

// buildLockInShareMode rewrites FOR SHARE to the pre-8.0 MySQL syntax.
func buildLockInShareMode(c clause.Clause, builder clause.Builder) {
	if values, ok := c.Expression.(clause.Locking); ok && strings.EqualFold(values.Strength, "SHARE") {
		writeString(builder, "LOCK IN SHARE MODE")
		return
	}
	c.Build(builder)
}

// mysqlQuoteTo quotes identifiers with backticks. Dots separate identifier
// parts; embedded backticks are escaped by doubling.
func mysqlQuoteTo(writer clause.Writer, str string) {
	var (
		underQuoted, selfQuoted bool
		continuousBacktick      int8
		shiftDelimiter          int8
	)

	for _, v := range []byte(str) {
		switch v {
		case '`':
			continuousBacktick++
			if continuousBacktick == 2 {
				writeString(writer, "``")
				continuousBacktick = 0
			}
		case '.':
			if continuousBacktick > 0 || !selfQuoted {
				shiftDelimiter = 0
				underQuoted = false
				continuousBacktick = 0
				writeByte(writer, '`')
			}
			writeByte(writer, v)
			continue
		default:
			if shiftDelimiter-continuousBacktick <= 0 && !underQuoted {
				writeByte(writer, '`')
				underQuoted = true
				if selfQuoted = continuousBacktick > 0; selfQuoted {
					continuousBacktick--
				}
			}

			for ; continuousBacktick > 0; continuousBacktick-- {
				writeString(writer, "``")
			}

			writeByte(writer, v)
		}
		shiftDelimiter++
	}

	if continuousBacktick > 0 && !selfQuoted {
		writeString(writer, "``")
	}
	writeByte(writer, '`')
}

// mysqlDataTypeOf maps a schema field to its MySQL-compatible column type.
func mysqlDataTypeOf(d *Dialector, field *schema.Field) string {
	switch field.DataType {
	case schema.Bool:
		return "boolean"
	case schema.Int, schema.Uint:
		return d.mysqlIntType(field)
	case schema.Float:
		return d.mysqlFloatType(field)
	case schema.String:
		return d.mysqlStringType(field)
	case schema.Time:
		return d.mysqlTimeType(field)
	case schema.Bytes:
		return d.mysqlBytesType(field)
	default:
		return d.mysqlCustomType(field)
	}
}

func (d *Dialector) mysqlIntType(field *schema.Field) string {
	constraint := func(sqlType string) string {
		if field.DataType == schema.Uint {
			sqlType += " unsigned"
		}
		if field.AutoIncrement {
			sqlType += " AUTO_INCREMENT"
		}
		return sqlType
	}

	switch {
	case field.Size <= 8:
		return constraint("tinyint")
	case field.Size <= 16:
		return constraint("smallint")
	case field.Size <= 24:
		return constraint("mediumint")
	case field.Size <= 32:
		return constraint("int")
	default:
		return constraint("bigint")
	}
}

func (d *Dialector) mysqlFloatType(field *schema.Field) string {
	if field.Precision > 0 {
		return fmt.Sprintf("decimal(%d, %d)", field.Precision, field.Scale)
	}
	if field.Size <= 32 {
		return "float"
	}
	return "double"
}

func (d *Dialector) mysqlStringType(field *schema.Field) string {
	size := field.Size
	if size == 0 {
		if d.DefaultStringSize > 0 {
			size = int(d.DefaultStringSize)
		} else {
			hasIndex := field.TagSettings["INDEX"] != "" || field.TagSettings["UNIQUE"] != ""
			// TEXT, GEOMETRY and JSON columns cannot carry a default value.
			if field.PrimaryKey || field.HasDefaultValue || hasIndex {
				size = 191 // utf8mb4
			}
		}
	}

	if size >= 65536 && size <= int(math.Pow(2, 24)) {
		return "mediumtext"
	}
	if size > int(math.Pow(2, 24)) || size <= 0 {
		return "longtext"
	}
	return fmt.Sprintf("varchar(%d)", size)
}

func (d *Dialector) mysqlTimeType(field *schema.Field) string {
	if !d.DisableDatetimePrecision && field.Precision == 0 && field.TagSettings["PRECISION"] == "" {
		if d.DefaultDatetimePrecision != nil {
			field.Precision = *d.DefaultDatetimePrecision
		} else {
			field.Precision = mysqlDatetimePrecision
		}
	}

	var precision string
	if field.Precision > 0 {
		precision = fmt.Sprintf("(%d)", field.Precision)
	}

	if field.NotNull || field.PrimaryKey {
		return "datetime" + precision
	}
	return "datetime" + precision + " NULL"
}

func (d *Dialector) mysqlBytesType(field *schema.Field) string {
	if field.Size > 0 && field.Size < 65536 {
		return fmt.Sprintf("varbinary(%d)", field.Size)
	}
	if field.Size >= 65536 && field.Size <= int(math.Pow(2, 24)) {
		return "mediumblob"
	}
	return "longblob"
}

func (d *Dialector) mysqlCustomType(field *schema.Field) string {
	sqlType := string(field.DataType)
	if field.AutoIncrement && !strings.Contains(strings.ToLower(sqlType), " auto_increment") {
		sqlType += " AUTO_INCREMENT"
	}
	return sqlType
}
