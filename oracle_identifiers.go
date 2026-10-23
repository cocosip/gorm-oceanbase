package oceanbase

import (
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

// oracleIdentifierName is the name returned by the dictionary and result sets.
func oracleIdentifierName(name string) string {
	if len(name) >= 2 && name[0] == '"' && name[len(name)-1] == '"' {
		return strings.ReplaceAll(name[1:len(name)-1], `""`, `"`)
	}
	return strings.ToUpper(name)
}

func oracleModelField(s *schema.Schema, name string) *schema.Field {
	if s != nil {
		for _, field := range s.Fields {
			if field.DBName != "" && oracleIdentifierName(field.DBName) == name {
				return field
			}
		}
	}
	return nil
}

// Keep model DBNames intact, including explicit quotes, when GORM scans results.
func mapOracleColumns(db *gorm.DB) {
	stmt := db.Statement
	if stmt.Schema == nil {
		return
	}
	mapping := make(map[string]string, len(stmt.ColumnMapping)+len(stmt.Schema.Fields))
	for from, to := range stmt.ColumnMapping {
		mapping[from] = to
	}
	add := func(s *schema.Schema, physicalPrefix, modelPrefix string) {
		for _, field := range s.Fields {
			if field.DBName == "" {
				continue
			}
			physical := oracleIdentifierName(physicalPrefix + field.DBName)
			if _, exists := mapping[physical]; !exists {
				mapping[physical] = modelPrefix + field.DBName
			}
		}
	}
	add(stmt.Schema, "", "")
	for _, join := range stmt.Joins {
		s := stmt.Schema
		prefix := ""
		path := strings.Split(join.Name, ".")
		for i, name := range path {
			relation := s.Relationships.Relations[name]
			if relation == nil {
				break
			}
			prefix += relation.Name + "__"
			s = relation.FieldSchema
			physical := prefix
			if i == len(path)-1 && join.Alias != "" {
				physical = join.Alias + "__"
			}
			add(s, physical, prefix)
		}
	}
	stmt.ColumnMapping = mapping
}
