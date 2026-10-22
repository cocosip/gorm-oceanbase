package oceanbase

import (
	"strings"
	"testing"

	"gorm.io/gorm"
)

type migrateUser struct {
	ID    uint   `gorm:"primaryKey"`
	Name  string `gorm:"size:64;index"`
	Email string `gorm:"size:128;uniqueIndex"`
	Age   int    `gorm:"default:18"`
}

func TestFullDataTypeOf(t *testing.T) {
	db := openWithFakeDB(t)
	stmt := &gorm.Statement{DB: db}
	if err := stmt.Parse(&migrateUser{}); err != nil {
		t.Fatalf("Parse: %v", err)
	}

	cases := []struct {
		field string
		want  string
	}{
		{field: "name", want: "varchar(64)"},
		{field: "age", want: "bigint DEFAULT 18"},
	}
	for _, tc := range cases {
		full := db.Migrator().FullDataTypeOf(stmt.Schema.FieldsByDBName[tc.field])
		if !strings.Contains(full.SQL, tc.want) {
			t.Fatalf("FullDataTypeOf(%s) = %q, want %q", tc.field, full.SQL, tc.want)
		}
	}
}

func TestFullDataTypeOfComment(t *testing.T) {
	db := openWithFakeDB(t)

	type commented struct {
		ID   uint   `gorm:"primaryKey"`
		Name string `gorm:"size:32;comment:display name"`
	}
	stmt := &gorm.Statement{DB: db}
	if err := stmt.Parse(&commented{}); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	full := db.Migrator().FullDataTypeOf(stmt.Schema.FieldsByDBName["name"])
	if !strings.Contains(full.SQL, "COMMENT 'display name'") {
		t.Fatalf("FullDataTypeOf(name) = %q, want a COMMENT clause", full.SQL)
	}
}

func TestGetTypeAliases(t *testing.T) {
	db := openWithFakeDB(t)
	aliases := db.Migrator().GetTypeAliases("bool")
	if len(aliases) == 0 || aliases[0] != "tinyint" {
		t.Fatalf("aliases = %v", aliases)
	}
	if aliases := db.Migrator().GetTypeAliases("varchar"); aliases != nil {
		t.Fatalf("unknown type aliases = %v, want nil", aliases)
	}
}
