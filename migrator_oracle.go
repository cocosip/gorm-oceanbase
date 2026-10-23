package oceanbase

import (
	"database/sql"
	"fmt"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/migrator"
	"gorm.io/gorm/schema"
)

// Oracle data dictionary queries. Views come from the Oracle compatibility
// surface (ALL_* tables); identifiers fold to upper case, so every lookup
// compares the folded name.
const (
	oracleCurrentSchemaSQL = "SELECT SYS_CONTEXT('USERENV','CURRENT_SCHEMA') FROM dual"

	oracleHasTableSQL      = "SELECT count(*) FROM all_tables WHERE owner = ? AND table_name = ?"
	oracleHasColumnSQL     = "SELECT count(*) FROM all_tab_columns WHERE owner = ? AND table_name = ? AND column_name = ?"
	oracleGetTablesSQL     = "SELECT table_name FROM all_tables WHERE owner = ?"
	oracleHasConstraintSQL = "SELECT count(*) FROM all_constraints WHERE owner = ? AND table_name = ? AND constraint_name = ?"
	oracleHasIndexSQL      = "SELECT count(*) FROM all_indexes WHERE table_owner = ? AND table_name = ? AND index_name = ?"

	oracleColumnTypesSQL = "SELECT column_name, data_default, CASE WHEN nullable = 'Y' THEN 1 ELSE 0 END AS nullable_flag, data_type, char_length, data_precision, data_scale FROM all_tab_columns WHERE owner = ? AND table_name = ? ORDER BY column_id"

	oracleIndexSQL = "SELECT ix.index_name, CASE WHEN ix.uniqueness = 'UNIQUE' THEN 1 ELSE 0 END AS unique_flag, c.constraint_type, ic.column_name" +
		" FROM all_indexes ix" +
		" LEFT JOIN all_constraints c ON c.owner = ix.table_owner AND c.table_name = ix.table_name AND c.constraint_name = ix.index_name AND c.constraint_type = 'P'" +
		" JOIN all_ind_columns ic ON ic.index_owner = ix.table_owner AND ic.index_name = ix.index_name" +
		" WHERE ix.table_owner = ? AND ix.table_name = ?" +
		" ORDER BY ix.index_name, ic.column_position"
)

// oracleMigrator implements gorm.Migrator for Oracle compatibility mode.
// Schema, column, constraint and index lookups run against the Oracle data
// dictionary; auto-increment fields create IDENTITY columns behind the
// verified version gate (design.md §6.1).
type oracleMigrator struct {
	migrator.Migrator
	dialector *Dialector
}

func newOracleMigrator(db *gorm.DB, dialector *Dialector) gorm.Migrator {
	return &oracleMigrator{
		Migrator: migrator.Migrator{
			Config: migrator.Config{DB: db, Dialector: dialector, CreateIndexAfterCreateTable: true},
		},
		dialector: dialector,
	}
}

// OracleSequenceMigrator is the Oracle sequence extension of the GORM
// migrator. It is not part of the generic gorm.Migrator interface; reach it
// with a type assertion on db.Migrator().
type OracleSequenceMigrator interface {
	// CreateSequence creates a named sequence in the current schema; the
	// identifier is quoted and uppercased like every unquoted identifier.
	CreateSequence(name string) error
	// HasSequence reports whether the sequence exists in the current schema.
	HasSequence(name string) bool
}

// CreateSequence implements OracleSequenceMigrator. Values are drawn in the
// caller's transaction with <name>.NEXTVAL before the insert (design.md §6.2).
func (m *oracleMigrator) CreateSequence(name string) error {
	return m.DB.Exec("CREATE SEQUENCE ?", clause.Column{Name: name}).Error
}

// HasSequence implements OracleSequenceMigrator. Unquoted identifiers fold to
// upper case, so the lookup compares the folded name.
func (m *oracleMigrator) HasSequence(name string) bool {
	var count int64
	_ = m.DB.Raw("SELECT count(*) FROM user_sequences WHERE sequence_name = ?", oracleIdentifierName(name)).
		Row().Scan(&count)
	return count > 0
}

// CurrentDatabase resolves the current schema.
func (m *oracleMigrator) CurrentDatabase() (name string) {
	_ = m.DB.Raw(oracleCurrentSchemaSQL).Row().Scan(&name)
	return
}

// CurrentSchema splits a schema-qualified table name or defaults to the
// current schema.
func (m *oracleMigrator) CurrentSchema(_ *gorm.Statement, table string) (string, string) {
	if tables := strings.Split(table, "."); len(tables) == 2 {
		return oracleIdentifierName(tables[0]), tables[1]
	}
	return m.CurrentDatabase(), table
}

// HasTable reports whether the table exists in the schema.
func (m *oracleMigrator) HasTable(value interface{}) bool {
	var count int64
	_ = m.RunWithValue(value, func(stmt *gorm.Statement) error {
		schemaName, tableName := m.CurrentSchema(stmt, stmt.Table)
		return m.DB.Raw(oracleHasTableSQL, schemaName, oracleIdentifierName(tableName)).Row().Scan(&count)
	})
	return count > 0
}

// HasColumn reports whether the column exists on the table.
func (m *oracleMigrator) HasColumn(value interface{}, field string) bool {
	var count int64
	_ = m.RunWithValue(value, func(stmt *gorm.Statement) error {
		schemaName, tableName := m.CurrentSchema(stmt, stmt.Table)
		name := field
		if stmt.Schema != nil {
			if f := stmt.Schema.LookUpField(field); f != nil {
				name = f.DBName
			}
		}
		return m.DB.Raw(oracleHasColumnSQL, schemaName, oracleIdentifierName(tableName), oracleIdentifierName(name)).Row().Scan(&count)
	})
	return count > 0
}

// GetTables lists the tables of the current schema.
func (m *oracleMigrator) GetTables() (tableList []string, err error) {
	err = m.DB.Raw(oracleGetTablesSQL, m.CurrentDatabase()).Scan(&tableList).Error
	return
}

// TableType reads table metadata from the data dictionary.
func (m *oracleMigrator) TableType(value interface{}) (gorm.TableType, error) {
	var table migrator.TableType
	err := m.RunWithValue(value, func(stmt *gorm.Statement) error {
		schemaName, tableName := m.CurrentSchema(stmt, stmt.Table)
		return m.DB.Raw(
			"SELECT t.owner, t.table_name, 'BASE TABLE', c.comments FROM all_tables t"+
				" LEFT JOIN all_tab_comments c ON c.owner = t.owner AND c.table_name = t.table_name"+
				" WHERE t.owner = ? AND t.table_name = ?",
			schemaName, oracleIdentifierName(tableName),
		).Row().Scan(&table.SchemaValue, &table.NameValue, &table.TypeValue, &table.CommentValue)
	})
	return table, err
}

// HasConstraint reports whether a named constraint exists on the table.
func (m *oracleMigrator) HasConstraint(value interface{}, name string) bool {
	var count int64
	_ = m.RunWithValue(value, func(stmt *gorm.Statement) error {
		schemaName, tableName := m.CurrentSchema(stmt, stmt.Table)
		if constraint, _ := m.GuessConstraintInterfaceAndTable(stmt, name); constraint != nil {
			name = constraint.GetName()
		}
		return m.DB.Raw(oracleHasConstraintSQL, schemaName, oracleIdentifierName(tableName), oracleIdentifierName(name)).Row().Scan(&count)
	})
	return count > 0
}

// ColumnTypes reads column metadata from all_tab_columns. Identity columns
// report their generated default, which doubles as the auto-increment flag.
func (m *oracleMigrator) ColumnTypes(value interface{}) ([]gorm.ColumnType, error) {
	columnTypes := make([]gorm.ColumnType, 0)
	err := m.RunWithValue(value, func(stmt *gorm.Statement) error {
		schemaName, tableName := m.CurrentSchema(stmt, stmt.Table)
		rows, err := m.DB.Table(tableName).Raw(oracleColumnTypesSQL, schemaName, oracleIdentifierName(tableName)).Rows()
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()

		for rows.Next() {
			var (
				column        migrator.ColumnType
				defaultValue  sql.NullString
				nullableFlag  sql.NullInt64
				charLength    sql.NullInt64
				dataPrecision sql.NullInt64
				dataScale     sql.NullInt64
			)
			if scanErr := rows.Scan(
				&column.NameValue, &defaultValue, &nullableFlag, &column.DataTypeValue,
				&charLength, &dataPrecision, &dataScale,
			); scanErr != nil {
				return scanErr
			}

			column.NullableValue = sql.NullBool{Bool: nullableFlag.Int64 == 1, Valid: nullableFlag.Valid}
			column.DefaultValueValue = sql.NullString{String: strings.TrimSpace(defaultValue.String), Valid: defaultValue.Valid}
			if strings.Contains(defaultValue.String, "ISEQ$$") {
				column.AutoIncrementValue = sql.NullBool{Bool: true, Valid: true}
			}
			column.LengthValue = charLength
			if !strings.Contains(column.DataTypeValue.String, "CHAR") && column.DataTypeValue.String != "RAW" {
				column.LengthValue = sql.NullInt64{}
			}
			column.DecimalSizeValue = dataPrecision
			column.ScaleValue = dataScale
			if field := oracleModelField(stmt.Schema, column.NameValue.String); field != nil {
				column.NameValue.String = field.DBName
			}
			value := column.DefaultValueValue.String
			if len(value) >= 2 && value[0] == '\'' && value[len(value)-1] == '\'' {
				column.DefaultValueValue.String = strings.ReplaceAll(value[1:len(value)-1], "''", "'")
			}

			columnTypes = append(columnTypes, column)
		}
		return rows.Err()
	})
	return columnTypes, err
}

// oracleIndex is one joined all_indexes / all_ind_columns row.
type oracleIndex struct {
	IndexName      string         `gorm:"column:INDEX_NAME"`
	UniqueFlag     int32          `gorm:"column:UNIQUE_FLAG"`
	ConstraintType sql.NullString `gorm:"column:CONSTRAINT_TYPE"`
	ColumnName     string         `gorm:"column:COLUMN_NAME"`
}

// GetIndexes lists the indexes of a table in ordinal column order.
func (m *oracleMigrator) GetIndexes(value interface{}) ([]gorm.Index, error) {
	indexes := make([]gorm.Index, 0)
	err := m.RunWithValue(value, func(stmt *gorm.Statement) error {
		schemaName, tableName := m.CurrentSchema(stmt, stmt.Table)
		result := make([]*oracleIndex, 0)
		if scanErr := m.DB.Table(tableName).Raw(oracleIndexSQL, schemaName, oracleIdentifierName(tableName)).Scan(&result).Error; scanErr != nil {
			return scanErr
		}

		indexMap, indexNames := groupOracleIndexes(result)
		for _, name := range indexNames {
			rows := indexMap[name]
			if len(rows) == 0 {
				continue
			}
			tempIdx := &migrator.Index{
				TableName:       tableName,
				NameValue:       name,
				PrimaryKeyValue: sql.NullBool{Bool: rows[0].ConstraintType.String == "P", Valid: rows[0].ConstraintType.Valid},
				UniqueValue:     sql.NullBool{Bool: rows[0].UniqueFlag == 1, Valid: true},
			}
			for _, r := range rows {
				tempIdx.ColumnList = append(tempIdx.ColumnList, r.ColumnName)
			}
			indexes = append(indexes, tempIdx)
		}
		return nil
	})
	return indexes, err
}

func groupOracleIndexes(indexList []*oracleIndex) (map[string][]*oracleIndex, []string) {
	columnIndexMap := make(map[string][]*oracleIndex, len(indexList))
	indexNames := make([]string, 0, len(indexList))
	for _, idx := range indexList {
		if _, ok := columnIndexMap[idx.IndexName]; !ok {
			indexNames = append(indexNames, idx.IndexName)
		}
		columnIndexMap[idx.IndexName] = append(columnIndexMap[idx.IndexName], idx)
	}
	return columnIndexMap, indexNames
}

// FullDataTypeOf appends the IDENTITY clause for auto-increment fields when
// the server version is verified; otherwise the clause stays off and the DDL
// paths refuse before executing (design.md §6.1).
func (m *oracleMigrator) FullDataTypeOf(field *schema.Field) clause.Expr {
	expr := m.Migrator.FullDataTypeOf(field)
	if field.AutoIncrement && m.dialector.oracleIdentitySupported() {
		// BY DEFAULT (not ON NULL): an explicit NULL is never auto-numbered.
		expr.SQL += " GENERATED BY DEFAULT AS IDENTITY"
	}
	return expr
}

// CreateTable refuses IDENTITY DDL up front on unknown or unverified versions.
func (m *oracleMigrator) CreateTable(values ...interface{}) error {
	if err := m.checkIdentityDDL(values); err != nil {
		return err
	}
	return m.Migrator.CreateTable(values...)
}

// AddColumn refuses auto-increment columns on unverified versions.
func (m *oracleMigrator) AddColumn(value interface{}, name string) error {
	if err := m.checkIdentityDDL([]interface{}{value}); err != nil {
		return err
	}
	return m.Migrator.AddColumn(value, name)
}

// AlterColumn refuses turning a column into auto-increment on unverified
// versions.
func (m *oracleMigrator) AlterColumn(value interface{}, field string) error {
	return m.RunWithValue(value, func(stmt *gorm.Statement) error {
		if stmt.Schema != nil {
			if f := stmt.Schema.LookUpField(field); f != nil {
				return m.DB.Exec("ALTER TABLE ? MODIFY (? ?)", m.CurrentTable(stmt), clause.Column{Name: f.DBName}, m.Migrator.FullDataTypeOf(f)).Error
			}
		}
		return fmt.Errorf("failed to look up field with name: %s", field)
	})
}

// MigrateColumn compares inferred integer precision rather than Go's zero tag value.
func (m *oracleMigrator) MigrateColumn(value interface{}, field *schema.Field, columnType gorm.ColumnType) error {
	copyField := *field
	if field.DataType == schema.Int || field.DataType == schema.Uint {
		copyField.Precision = oracleNumberPrecision(field)
	}
	return m.Migrator.MigrateColumn(value, &copyField, columnType)
}

// HasIndex uses the Oracle dictionary and accepts model field names like GORM.
func (m *oracleMigrator) HasIndex(value interface{}, name string) bool {
	var count int64
	_ = m.RunWithValue(value, func(stmt *gorm.Statement) error {
		if stmt.Schema != nil {
			if idx := stmt.Schema.LookIndex(name); idx != nil {
				name = idx.Name
			}
		}
		owner, table := m.CurrentSchema(stmt, stmt.Table)
		return m.DB.Raw(oracleHasIndexSQL, owner, oracleIdentifierName(table), oracleIdentifierName(name)).Row().Scan(&count)
	})
	return count > 0
}

// DropIndex and RenameIndex use schema-level Oracle index names.
func (m *oracleMigrator) DropIndex(value interface{}, name string) error {
	return m.RunWithValue(value, func(stmt *gorm.Statement) error {
		if stmt.Schema != nil {
			if idx := stmt.Schema.LookIndex(name); idx != nil {
				name = idx.Name
			}
		}
		return m.DB.Exec("DROP INDEX ?", clause.Column{Name: name}).Error
	})
}

func (m *oracleMigrator) RenameIndex(value interface{}, oldName, newName string) error {
	return m.RunWithValue(value, func(_ *gorm.Statement) error {
		return m.DB.Exec("ALTER INDEX ? RENAME TO ?", clause.Column{Name: oldName}, clause.Column{Name: newName}).Error
	})
}

// checkIdentityDDL walks the models and refuses any IDENTITY-producing DDL
// before a single statement executes (design.md §6.1).
func (m *oracleMigrator) checkIdentityDDL(values []interface{}) error {
	for _, value := range values {
		err := m.RunWithValue(value, func(stmt *gorm.Statement) error {
			if stmt.Schema == nil {
				return nil
			}
			for _, field := range stmt.Schema.Fields {
				if field.AutoIncrement && !m.dialector.oracleIdentitySupported() {
					return fmt.Errorf("%w: server version %q; use an explicit primary key or a sequence instead", ErrIdentityUnsupported, m.dialector.ServerVersion)
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// DropTable drops tables without the IF EXISTS clause Oracle lacks; missing
// tables are skipped via the data dictionary.
func (m *oracleMigrator) DropTable(values ...interface{}) error {
	values = m.ReorderModels(values, false)
	for i := len(values) - 1; i >= 0; i-- {
		value := values[i]
		if !m.HasTable(value) {
			continue
		}
		err := m.RunWithValue(value, func(stmt *gorm.Statement) error {
			return m.DB.Exec("DROP TABLE ? CASCADE CONSTRAINTS", m.CurrentTable(stmt)).Error
		})
		if err != nil {
			return err
		}
	}
	return nil
}

var (
	_ gorm.Migrator          = (*oracleMigrator)(nil)
	_ OracleSequenceMigrator = (*oracleMigrator)(nil)
)
