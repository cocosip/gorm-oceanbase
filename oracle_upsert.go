package oceanbase

import (
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/callbacks"
	"gorm.io/gorm/clause"
)

// Keep one DEFAULT row per input even when GORM omits every column.
func oracleCreateValues(db *gorm.DB) clause.Values {
	values := callbacks.ConvertToCreateValues(db.Statement)
	if db.Error != nil || len(values.Columns) != 0 {
		return values
	}
	field := firstOracleDefaultField(db.Statement)
	if field == nil {
		_ = db.AddError(gorm.ErrInvalidData)
		return values
	}
	values.Columns = []clause.Column{{Name: field.DBName}}
	for i := range values.Values {
		values.Values[i] = []interface{}{oracleDefaultMarker{}}
	}
	return values
}

func oracleNeedsGeneratedPK(columns []clause.Column, values []interface{}, name string) bool {
	for i, column := range columns {
		if oracleIdentifierName(column.Name) == oracleIdentifierName(name) {
			_, marker := values[i].(oracleDefaultMarker)
			return marker
		}
	}
	return true
}

// Explicit ON-column assignments are rejected; only GORM's expansion is filtered.
func oracleFilterONAssignments(assignments clause.Set, explicit int, target []clause.Column) clause.Set {
	filtered := make(clause.Set, 0, len(assignments))
	for i, assignment := range assignments {
		if i < explicit || !oracleRowHasColumn(target, assignment.Column.Name) {
			filtered = append(filtered, assignment)
		}
	}
	return filtered
}

func oracleSameColumns(target []clause.Column, names []string) bool {
	if len(target) != len(names) {
		return false
	}
	for _, name := range names {
		if !oracleRowHasColumn(target, name) {
			return false
		}
	}
	return true
}

// Model primary keys follow GORM's contract. Other targets must match a complete
// live unique index; DryRun uses model declarations because it cannot query.
func validateOracleConflictKey(db *gorm.DB, target []clause.Column) error {
	s := db.Statement.Schema
	if s != nil {
		primary := make([]string, 0, len(s.PrimaryFields))
		for _, field := range s.PrimaryFields {
			primary = append(primary, field.DBName)
		}
		if oracleSameColumns(target, primary) {
			return nil
		}
	}
	if db.DryRun {
		if s != nil {
			for _, constraint := range s.ParseUniqueConstraints() {
				if oracleSameColumns(target, []string{constraint.Field.DBName}) {
					return nil
				}
			}
			for _, index := range s.ParseIndexes() {
				if index.Class != "UNIQUE" || index.Where != "" {
					continue
				}
				columns := make([]string, 0, len(index.Fields))
				for _, field := range index.Fields {
					if field.Expression == "" {
						columns = append(columns, field.DBName)
					}
				}
				if len(columns) == len(index.Fields) && oracleSameColumns(target, columns) {
					return nil
				}
			}
		}
	} else {
		table := db.Statement.Table
		if expr := db.Statement.TableExpr; expr != nil && len(expr.Vars) == 0 {
			table = expr.SQL
		}
		indexes, err := db.Session(&gorm.Session{NewDB: true}).Migrator().GetIndexes(table)
		if err != nil {
			return err
		}
		for _, index := range indexes {
			if unique, ok := index.Unique(); ok && unique && oracleSamePhysicalColumns(target, index.Columns()) {
				return nil
			}
		}
	}
	return fmt.Errorf("%w: conflict target must match a complete primary or unique key", ErrUpsertUnsupported)
}

func oracleSamePhysicalColumns(target []clause.Column, names []string) bool {
	if len(target) != len(names) {
		return false
	}
	for _, column := range target {
		found := false
		for _, name := range names {
			if oracleIdentifierName(column.Name) == name {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
