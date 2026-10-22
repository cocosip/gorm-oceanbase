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

// mysqlIndexSQL lists index columns for one table in ordinal order.
const mysqlIndexSQL = `
SELECT
	TABLE_NAME,
	COLUMN_NAME,
	INDEX_NAME,
	NON_UNIQUE
FROM
	information_schema.STATISTICS
WHERE
	TABLE_SCHEMA = ?
	AND TABLE_NAME = ?
ORDER BY
	INDEX_NAME,
	SEQ_IN_INDEX`

// mysqlTypeAliases maps database type names to dialect aliases used when
// comparing an existing column against the desired type.
var mysqlTypeAliases = map[string][]string{
	"bool":    {"tinyint"},
	"tinyint": {"bool"},
}

// mysqlMigrator implements gorm.Migrator for MySQL compatibility mode.
type mysqlMigrator struct {
	migrator.Migrator
	dialector *Dialector
}

func newMySQLMigrator(db *gorm.DB, dialector *Dialector) gorm.Migrator {
	return &mysqlMigrator{
		Migrator: migrator.Migrator{
			Config: migrator.Config{DB: db, Dialector: dialector},
		},
		dialector: dialector,
	}
}

// FullDataTypeOf appends the MySQL column COMMENT clause.
func (m *mysqlMigrator) FullDataTypeOf(field *schema.Field) clause.Expr {
	expr := m.Migrator.FullDataTypeOf(field)
	if value, ok := field.TagSettings["COMMENT"]; ok {
		expr.SQL += " COMMENT " + m.Explain("?", value)
	}
	return expr
}

// MigrateColumnUnique keeps the UNIQUE constraint in sync with the schema.
// In MySQL a unique index is reported through ColumnType.Unique, so redundant
// unique indexes must be cleaned up separately from the constraint.
func (m *mysqlMigrator) MigrateColumnUnique(value interface{}, field *schema.Field, columnType gorm.ColumnType) error {
	unique, ok := columnType.Unique()
	if !ok || field.PrimaryKey {
		return nil
	}

	queryTx, execTx := m.GetQueryAndExecTx()
	return m.RunWithValue(value, func(stmt *gorm.Statement) error {
		constraint := m.DB.NamingStrategy.UniqueName(stmt.Table, field.DBName)
		if unique {
			indexes, _ := queryTx.Migrator().GetIndexes(value)
			for _, index := range indexes {
				if uni, ok := index.Unique(); !ok || !uni {
					continue
				}
				if columns := index.Columns(); len(columns) != 1 || columns[0] != field.DBName {
					continue
				}
				if name := index.Name(); name == constraint || name == field.UniqueIndex {
					continue
				}
				if err := execTx.Migrator().DropIndex(value, index.Name()); err != nil {
					return err
				}
			}

			hasConstraint := queryTx.Migrator().HasConstraint(value, constraint)
			switch {
			case field.Unique && !hasConstraint:
				if err := execTx.Migrator().CreateConstraint(value, constraint); err != nil {
					return err
				}
			case !field.Unique && hasConstraint:
				if err := execTx.Migrator().DropConstraint(value, constraint); err != nil {
					return err
				}
				if field.UniqueIndex != "" {
					if err := execTx.Migrator().CreateIndex(value, field.UniqueIndex); err != nil {
						return err
					}
				}
			}

			if field.UniqueIndex != "" && !queryTx.Migrator().HasIndex(value, field.UniqueIndex) {
				if err := execTx.Migrator().CreateIndex(value, field.UniqueIndex); err != nil {
					return err
				}
			}
			return nil
		}

		if field.Unique {
			if err := execTx.Migrator().CreateConstraint(value, constraint); err != nil {
				return err
			}
		}
		if field.UniqueIndex != "" && !queryTx.Migrator().HasIndex(value, field.UniqueIndex) {
			if err := execTx.Migrator().CreateIndex(value, field.UniqueIndex); err != nil {
				return err
			}
		}
		return nil
	})
}

// AddColumn adds one column, including the primary key clause required by
// MySQL when an auto-increment column is added.
func (m *mysqlMigrator) AddColumn(value interface{}, name string) error {
	return m.RunWithValue(value, func(stmt *gorm.Statement) error {
		if stmt.Schema == nil {
			return fmt.Errorf("failed to look up field with name: %s", name)
		}
		f := stmt.Schema.LookUpField(name)
		if f == nil {
			return fmt.Errorf("failed to look up field with name: %s", name)
		}
		if f.IgnoreMigration {
			return nil
		}

		fieldType := m.FullDataTypeOf(f)
		columnName := clause.Column{Name: f.DBName}
		values := []interface{}{m.CurrentTable(stmt), columnName, fieldType}
		var alterSQL strings.Builder
		alterSQL.WriteString("ALTER TABLE ? ADD ? ?")
		if f.PrimaryKey || strings.Contains(strings.ToLower(fieldType.SQL), "auto_increment") {
			alterSQL.WriteString(", ADD PRIMARY KEY (?)")
			values = append(values, columnName)
		}
		return m.DB.Exec(alterSQL.String(), values...).Error
	})
}

// AlterColumn changes one column definition.
func (m *mysqlMigrator) AlterColumn(value interface{}, field string) error {
	return m.RunWithValue(value, func(stmt *gorm.Statement) error {
		if stmt.Schema == nil {
			return fmt.Errorf("failed to look up field with name: %s", field)
		}
		f := stmt.Schema.LookUpField(field)
		if f == nil {
			return fmt.Errorf("failed to look up field with name: %s", field)
		}

		fullDataType := m.FullDataTypeOf(f)
		if m.dialector.DontSupportRenameColumnUnique {
			fullDataType.SQL = strings.Replace(fullDataType.SQL, " UNIQUE ", " ", 1)
		}
		return m.DB.Exec(
			"ALTER TABLE ? MODIFY COLUMN ? ?",
			m.CurrentTable(stmt), clause.Column{Name: f.DBName}, fullDataType,
		).Error
	})
}

// RenameColumn renames a column, falling back to CHANGE when the server has no
// RENAME COLUMN syntax.
func (m *mysqlMigrator) RenameColumn(value interface{}, oldName, newName string) error {
	return m.RunWithValue(value, func(stmt *gorm.Statement) error {
		if !m.dialector.DontSupportRenameColumn {
			return m.Migrator.RenameColumn(value, oldName, newName)
		}

		var field *schema.Field
		if stmt.Schema != nil {
			if f := stmt.Schema.LookUpField(oldName); f != nil {
				oldName = f.DBName
				field = f
			}
			if f := stmt.Schema.LookUpField(newName); f != nil {
				newName = f.DBName
				field = f
			}
		}
		if field == nil {
			return fmt.Errorf("failed to look up field with name: %s", newName)
		}
		return m.DB.Exec(
			"ALTER TABLE ? CHANGE ? ? ?",
			m.CurrentTable(stmt), clause.Column{Name: oldName},
			clause.Column{Name: newName}, m.FullDataTypeOf(field),
		).Error
	})
}

// DropConstraint drops a foreign key, check constraint or index depending on
// the syntax the server supports.
func (m *mysqlMigrator) DropConstraint(value interface{}, name string) error {
	if !m.dialector.DontSupportDropConstraint {
		return m.Migrator.DropConstraint(value, name)
	}

	return m.RunWithValue(value, func(stmt *gorm.Statement) error {
		constraint, table := m.GuessConstraintInterfaceAndTable(stmt, name)
		if constraint != nil {
			name = constraint.GetName()
			switch constraint.(type) {
			case *schema.Constraint:
				return m.DB.Exec("ALTER TABLE ? DROP FOREIGN KEY ?", clause.Table{Name: table}, clause.Column{Name: name}).Error
			case *schema.CheckConstraint:
				return m.DB.Exec("ALTER TABLE ? DROP CHECK ?", clause.Table{Name: table}, clause.Column{Name: name}).Error
			}
		}
		if m.HasIndex(value, name) {
			return m.DB.Exec("ALTER TABLE ? DROP INDEX ?", clause.Table{Name: table}, clause.Column{Name: name}).Error
		}
		return nil
	})
}

// RenameIndex renames an index, recreating it when RENAME INDEX is missing.
func (m *mysqlMigrator) RenameIndex(value interface{}, oldName, newName string) error {
	if !m.dialector.DontSupportRenameIndex {
		return m.RunWithValue(value, func(stmt *gorm.Statement) error {
			return m.DB.Exec(
				"ALTER TABLE ? RENAME INDEX ? TO ?",
				m.CurrentTable(stmt), clause.Column{Name: oldName}, clause.Column{Name: newName},
			).Error
		})
	}

	return m.RunWithValue(value, func(stmt *gorm.Statement) error {
		if err := m.DropIndex(value, oldName); err != nil {
			return err
		}
		if stmt.Schema != nil {
			if idx := stmt.Schema.LookIndex(newName); idx == nil {
				if idx = stmt.Schema.LookIndex(oldName); idx != nil {
					opts := m.BuildIndexOptions(idx.Fields, stmt)
					values := []interface{}{clause.Column{Name: newName}, m.CurrentTable(stmt), opts}

					createIndexSQL := "CREATE "
					if idx.Class != "" {
						createIndexSQL += idx.Class + " "
					}
					createIndexSQL += "INDEX ? ON ??"
					if idx.Type != "" {
						createIndexSQL += " USING " + idx.Type
					}
					return m.DB.Exec(createIndexSQL, values...).Error
				}
			}
		}
		return m.CreateIndex(value, newName)
	})
}

// DropTable drops tables with foreign key checks disabled.
func (m *mysqlMigrator) DropTable(values ...interface{}) error {
	values = m.ReorderModels(values, false)
	return m.DB.Connection(func(tx *gorm.DB) error {
		tx.Exec("SET FOREIGN_KEY_CHECKS = 0;")
		for i := len(values) - 1; i >= 0; i-- {
			if err := m.RunWithValue(values[i], func(stmt *gorm.Statement) error {
				return tx.Exec("DROP TABLE IF EXISTS ? CASCADE", m.CurrentTable(stmt)).Error
			}); err != nil {
				return err
			}
		}
		return tx.Exec("SET FOREIGN_KEY_CHECKS = 1;").Error
	})
}

// ColumnTypes reads column metadata from information_schema.
func (m *mysqlMigrator) ColumnTypes(value interface{}) ([]gorm.ColumnType, error) {
	columnTypes := make([]gorm.ColumnType, 0)
	err := m.RunWithValue(value, func(stmt *gorm.Statement) error {
		var (
			currentDatabase, table = m.CurrentSchema(stmt, stmt.Table)
			columnTypeSQL          = "SELECT column_name, column_default, is_nullable = 'YES', data_type, character_maximum_length, column_type, column_key, extra, column_comment, numeric_precision, numeric_scale "
		)
		rows, err := m.DB.Session(&gorm.Session{}).Table(table).Limit(1).Rows()
		if err != nil {
			return err
		}
		rawColumnTypes, err := rows.ColumnTypes()
		if err != nil {
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}

		if !m.dialector.DisableDatetimePrecision {
			columnTypeSQL += ", datetime_precision "
		}
		columnTypeSQL += "FROM information_schema.columns WHERE table_schema = ? AND table_name = ? ORDER BY ORDINAL_POSITION"

		columns, rowErr := m.DB.Table(table).Raw(columnTypeSQL, currentDatabase, table).Rows()
		if rowErr != nil {
			return rowErr
		}
		defer func() { _ = columns.Close() }()

		for columns.Next() {
			var (
				column            migrator.ColumnType
				datetimePrecision sql.NullInt64
				extraValue        sql.NullString
				columnKey         sql.NullString
				values            = []interface{}{
					&column.NameValue, &column.DefaultValueValue, &column.NullableValue, &column.DataTypeValue,
					&column.LengthValue, &column.ColumnTypeValue, &columnKey, &extraValue, &column.CommentValue,
					&column.DecimalSizeValue, &column.ScaleValue,
				}
			)
			if !m.dialector.DisableDatetimePrecision {
				values = append(values, &datetimePrecision)
			}
			if scanErr := columns.Scan(values...); scanErr != nil {
				return scanErr
			}

			column.PrimaryKeyValue = sql.NullBool{Bool: false, Valid: true}
			column.UniqueValue = sql.NullBool{Bool: false, Valid: true}
			switch columnKey.String {
			case "PRI":
				column.PrimaryKeyValue = sql.NullBool{Bool: true, Valid: true}
			case "UNI":
				column.UniqueValue = sql.NullBool{Bool: true, Valid: true}
			}
			if strings.Contains(extraValue.String, "auto_increment") {
				column.AutoIncrementValue = sql.NullBool{Bool: true, Valid: true}
			}

			// Only paired single quotes around a literal default are trimmed.
			value := column.DefaultValueValue.String
			for (len(value) >= 3 && value[0] == '\'' && value[len(value)-1] == '\'' && value[len(value)-2] != '\\') ||
				(len(value) == 2 && value == "''") {
				value = value[1 : len(value)-1]
			}
			column.DefaultValueValue.String = value

			if m.dialector.DontSupportNullAsDefaultValue {
				if column.DefaultValueValue.Valid && column.DefaultValueValue.String == "NULL" {
					column.DefaultValueValue.Valid = false
					column.DefaultValueValue.String = ""
				}
			}
			if datetimePrecision.Valid {
				column.DecimalSizeValue = datetimePrecision
			}

			for _, c := range rawColumnTypes {
				if c.Name() == column.NameValue.String {
					column.SQLColumnType = c
					break
				}
			}
			columnTypes = append(columnTypes, column)
		}
		return nil
	})
	return columnTypes, err
}

// CurrentDatabase resolves the current database, preferring the longest
// matching schema name.
func (m *mysqlMigrator) CurrentDatabase() (name string) {
	baseName := m.Migrator.CurrentDatabase()
	m.DB.Raw(
		"SELECT SCHEMA_NAME from Information_schema.SCHEMATA where SCHEMA_NAME LIKE ? ORDER BY SCHEMA_NAME=? DESC,SCHEMA_NAME limit 1",
		baseName+"%", baseName).Scan(&name)
	return
}

// GetTables lists the tables of the current schema.
func (m *mysqlMigrator) GetTables() (tableList []string, err error) {
	err = m.DB.Raw("SELECT TABLE_NAME FROM information_schema.tables where TABLE_SCHEMA=?", m.CurrentDatabase()).
		Scan(&tableList).Error
	return
}

// GetIndexes lists the indexes of a table.
func (m *mysqlMigrator) GetIndexes(value interface{}) ([]gorm.Index, error) {
	indexes := make([]gorm.Index, 0)
	err := m.RunWithValue(value, func(stmt *gorm.Statement) error {
		result := make([]*mysqlIndex, 0)
		schemaName, table := m.CurrentSchema(stmt, stmt.Table)
		if scanErr := m.DB.Table(table).Raw(mysqlIndexSQL, schemaName, table).Scan(&result).Error; scanErr != nil {
			return scanErr
		}
		indexMap, indexNames := groupByMySQLIndexName(result)

		for _, name := range indexNames {
			idx := indexMap[name]
			if len(idx) == 0 {
				continue
			}
			tempIdx := &migrator.Index{
				TableName: idx[0].TableName,
				NameValue: idx[0].IndexName,
				PrimaryKeyValue: sql.NullBool{
					Bool:  idx[0].IndexName == "PRIMARY",
					Valid: true,
				},
				UniqueValue: sql.NullBool{
					Bool:  idx[0].NonUnique == 0,
					Valid: true,
				},
			}
			for _, x := range idx {
				tempIdx.ColumnList = append(tempIdx.ColumnList, x.ColumnName)
			}
			indexes = append(indexes, tempIdx)
		}
		return nil
	})
	return indexes, err
}

// mysqlIndex is one information_schema.STATISTICS row.
type mysqlIndex struct {
	TableName  string `gorm:"column:TABLE_NAME"`
	ColumnName string `gorm:"column:COLUMN_NAME"`
	IndexName  string `gorm:"column:INDEX_NAME"`
	NonUnique  int32  `gorm:"column:NON_UNIQUE"`
}

func groupByMySQLIndexName(indexList []*mysqlIndex) (map[string][]*mysqlIndex, []string) {
	columnIndexMap := make(map[string][]*mysqlIndex, len(indexList))
	indexNames := make([]string, 0, len(indexList))
	for _, idx := range indexList {
		if _, ok := columnIndexMap[idx.IndexName]; !ok {
			indexNames = append(indexNames, idx.IndexName)
		}
		columnIndexMap[idx.IndexName] = append(columnIndexMap[idx.IndexName], idx)
	}
	return columnIndexMap, indexNames
}

// CurrentSchema splits a qualified table name into schema and table.
func (m *mysqlMigrator) CurrentSchema(_ *gorm.Statement, table string) (string, string) {
	if tables := strings.Split(table, "."); len(tables) == 2 {
		return tables[0], tables[1]
	}
	m.DB = m.DB.Table(table)
	return m.CurrentDatabase(), table
}

// GetTypeAliases returns equivalent database type names.
func (m *mysqlMigrator) GetTypeAliases(databaseTypeName string) []string {
	return mysqlTypeAliases[databaseTypeName]
}

// TableType reads table metadata from information_schema.
func (m *mysqlMigrator) TableType(value interface{}) (tableType gorm.TableType, err error) {
	var table migrator.TableType
	err = m.RunWithValue(value, func(stmt *gorm.Statement) error {
		values := []interface{}{
			&table.SchemaValue, &table.NameValue, &table.TypeValue, &table.CommentValue,
		}
		currentDatabase, tableName := m.CurrentSchema(stmt, stmt.Table)
		tableTypeSQL := "SELECT table_schema, table_name, table_type, table_comment FROM information_schema.tables WHERE table_schema = ? AND table_name = ?"
		row := m.DB.Table(tableName).Raw(tableTypeSQL, currentDatabase, tableName).Row()
		return row.Scan(values...)
	})
	return table, err
}

// HasTable reports whether the table exists.
func (m *mysqlMigrator) HasTable(value interface{}) bool {
	var count int64
	_ = m.RunWithValue(value, func(stmt *gorm.Statement) error {
		currentDatabase := m.DB.Migrator().CurrentDatabase()
		return m.DB.Raw(
			"SELECT count(*) FROM information_schema.tables WHERE table_schema = ? AND table_name = ? AND table_type = ?",
			currentDatabase, stmt.Table, "BASE TABLE").Row().Scan(&count)
	})
	return count > 0
}

// HasColumn reports whether the column exists.
func (m *mysqlMigrator) HasColumn(value interface{}, field string) bool {
	var count int64
	_ = m.RunWithValue(value, func(stmt *gorm.Statement) error {
		currentDatabase := m.DB.Migrator().CurrentDatabase()
		name := field
		if stmt.Schema != nil {
			if f := stmt.Schema.LookUpField(field); f != nil {
				name = f.DBName
			}
		}
		return m.DB.Raw(
			"SELECT count(*) FROM INFORMATION_SCHEMA.columns WHERE table_schema = ? AND table_name = ? AND column_name = ?",
			currentDatabase, stmt.Table, name).Row().Scan(&count)
	})
	return count > 0
}

var _ gorm.Migrator = (*mysqlMigrator)(nil)
