package oceanbase

import (
	"strings"
	"testing"

	ob "github.com/cocosip/go-oceanbase"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

func TestMySQLDataTypeOf(t *testing.T) {
	d := &Dialector{Config: &Config{resolvedMode: ob.ModeMySQL}}

	cases := []struct {
		name  string
		field schema.Field
		want  string
	}{
		{name: "bool", field: schema.Field{DataType: schema.Bool}, want: "boolean"},
		{name: "int8", field: schema.Field{DataType: schema.Int, Size: 8}, want: "tinyint"},
		{name: "int16", field: schema.Field{DataType: schema.Int, Size: 16}, want: "smallint"},
		{name: "int24", field: schema.Field{DataType: schema.Int, Size: 24}, want: "mediumint"},
		{name: "int32", field: schema.Field{DataType: schema.Int, Size: 32}, want: "int"},
		{name: "int64", field: schema.Field{DataType: schema.Int, Size: 64}, want: "bigint"},
		{name: "uint auto", field: schema.Field{DataType: schema.Uint, Size: 64, AutoIncrement: true}, want: "bigint unsigned AUTO_INCREMENT"},
		{name: "float", field: schema.Field{DataType: schema.Float, Size: 32}, want: "float"},
		{name: "double", field: schema.Field{DataType: schema.Float, Size: 64}, want: "double"},
		{name: "decimal", field: schema.Field{DataType: schema.Float, Precision: 10, Scale: 2}, want: "decimal(10, 2)"},
		{name: "plain string", field: schema.Field{DataType: schema.String}, want: "longtext"},
		{name: "sized string", field: schema.Field{DataType: schema.String, Size: 64}, want: "varchar(64)"},
		{name: "primary string", field: schema.Field{DataType: schema.String, PrimaryKey: true}, want: testVarchar191},
		{name: "default string", field: schema.Field{DataType: schema.String, HasDefaultValue: true}, want: testVarchar191},
		{name: "indexed string", field: schema.Field{DataType: schema.String, TagSettings: map[string]string{"INDEX": "idx_name"}}, want: testVarchar191},
		{name: "unique string", field: schema.Field{DataType: schema.String, TagSettings: map[string]string{"UNIQUE": "uni_name"}}, want: testVarchar191},
		{name: "medium text", field: schema.Field{DataType: schema.String, Size: 65536}, want: "mediumtext"},
		{name: "long text", field: schema.Field{DataType: schema.String, Size: 16777217}, want: "longtext"},
		{name: "bytes", field: schema.Field{DataType: schema.Bytes, Size: 32}, want: "varbinary(32)"},
		{name: "medium blob", field: schema.Field{DataType: schema.Bytes, Size: 70000}, want: "mediumblob"},
		{name: "long blob", field: schema.Field{DataType: schema.Bytes}, want: "longblob"},
		{name: "time", field: schema.Field{DataType: schema.Time, Precision: 3}, want: "datetime(3) NULL"},
		{name: "time not null", field: schema.Field{DataType: schema.Time, Precision: 3, NotNull: true}, want: "datetime(3)"},
		{name: "custom", field: schema.Field{DataType: "json"}, want: "json"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := d.DataTypeOf(&tc.field); got != tc.want {
				t.Fatalf("DataTypeOf() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestMySQLDataTypeOfDefaultStringSize(t *testing.T) {
	d := &Dialector{Config: &Config{DefaultStringSize: 255}}
	field := &schema.Field{DataType: schema.String}
	if got := d.DataTypeOf(field); got != "varchar(255)" {
		t.Fatalf("DataTypeOf() = %q, want varchar(255)", got)
	}
}

func TestMySQLDryRunSQL(t *testing.T) {
	db := openWithFakeDB(t)
	dry := db.Session(&gorm.Session{DryRun: true})

	type user struct {
		ID   uint   `gorm:"primaryKey"`
		Name string `gorm:"size:64"`
	}

	createSQL := dry.Create(&user{Name: "alice"}).Statement.SQL.String()
	if createSQL != "INSERT INTO `users` (`name`) VALUES (?)" {
		t.Fatalf("create SQL = %q", createSQL)
	}

	querySQL := dry.Model(&user{}).Where("name = ?", "alice").Limit(5).Offset(2).Find(&[]user{}).Statement.SQL.String()
	wantQuery := "SELECT * FROM `users` WHERE name = ? LIMIT ? OFFSET ?"
	if querySQL != wantQuery {
		t.Fatalf("query SQL = %q, want %q", querySQL, wantQuery)
	}

	updateSQL := dry.Model(&user{}).Where("id = ?", 1).Update("name", "bob").Statement.SQL.String()
	if updateSQL != "UPDATE `users` SET `name`=? WHERE id = ?" {
		t.Fatalf("update SQL = %q", updateSQL)
	}

	deleteSQL := dry.Where("id = ?", 1).Delete(&user{}).Statement.SQL.String()
	if deleteSQL != "DELETE FROM `users` WHERE id = ?" {
		t.Fatalf("delete SQL = %q", deleteSQL)
	}
}

func TestMySQLDryRunDateFormat(t *testing.T) {
	db := openWithFakeDB(t)
	explained := db.ToSQL(func(tx *gorm.DB) *gorm.DB {
		type event struct {
			ID        uint
			CreatedAt int64
		}
		return tx.Model(&event{}).Where("id = ?", 7).Find(&event{})
	})
	if !strings.Contains(explained, "SELECT * FROM `events` WHERE id = 7") {
		t.Fatalf("ToSQL = %q", explained)
	}
}
