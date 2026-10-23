package oceanbase

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"reflect"
	"strings"
	"testing"

	"gorm.io/gorm"
	"gorm.io/gorm/migrator"
)

const (
	oracleReviewTable          = "review_rows"
	oracleQuotedRows           = "Rows"
	oracleOldDefault           = "'old'"
	oracleQualifiedReviewTable = "other.review_rows"
)

type oracleQualifiedUniqueRow struct {
	Name string `gorm:"size:64;unique"`
}

func (oracleQualifiedUniqueRow) TableName() string { return oracleQualifiedReviewTable }

type oracleQualifiedOrdinaryRow struct {
	Name string `gorm:"size:64"`
}

func (oracleQualifiedOrdinaryRow) TableName() string { return oracleQualifiedReviewTable }

func TestOracleQualifiedModelUniqueConstraint(t *testing.T) {
	db, _, pool := openOracleWithFakeDB(t)
	if err := db.Migrator().CreateTable(&oracleQualifiedUniqueRow{}); err != nil {
		t.Fatal(err)
	}
	const constraint = `"UNI_OTHER_REVIEW_ROWS_NAME"`
	if got := pool.server.recordedExecs(); len(got) != 1 || !strings.Contains(got[0], "CONSTRAINT "+constraint+" UNIQUE") {
		t.Fatalf("create unique: %v", got)
	}
	pool.server.reply(oracleHasTableSQL, "1")
	pool.server.reply(oracleHasConstraintSQL, "0")
	pool.server.replyRows(oracleColumnTypesSQL, testOracleColumnNames, [][]driver.Value{
		{testPhysicalName, nil, int64(1), testOracleVarchar2, int64(64), nil, nil},
	})
	if err := db.AutoMigrate(&oracleQualifiedUniqueRow{}); err != nil {
		t.Fatal(err)
	}
	wantAdd := `ALTER TABLE "OTHER"."REVIEW_ROWS" ADD CONSTRAINT ` + constraint + ` UNIQUE ("NAME")`
	if got := pool.server.recordedExecs(); len(got) != 2 || got[1] != wantAdd {
		t.Fatalf("add unique: %v", got)
	}
	pool.server.reply(oracleHasConstraintSQL, "1")
	if err := db.AutoMigrate(&oracleQualifiedUniqueRow{}); err != nil {
		t.Fatal(err)
	}
	if got := pool.server.recordedExecs(); len(got) != 2 {
		t.Fatalf("repeated migration: %v", got)
	}
	if err := db.AutoMigrate(&oracleQualifiedOrdinaryRow{}); err != nil {
		t.Fatal(err)
	}
	wantDrop := `ALTER TABLE "OTHER"."REVIEW_ROWS" DROP CONSTRAINT ` + constraint
	if got := pool.server.recordedExecs(); len(got) != 3 || got[2] != wantDrop {
		t.Fatalf("drop unique: %v", got)
	}
}

func TestOracleMigrateUniqueConstraint(t *testing.T) {
	type unique struct {
		ID   int64  `gorm:"primaryKey;autoIncrement:false"`
		Name string `gorm:"size:64;unique"`
	}
	type ordinary struct {
		ID   int64  `gorm:"primaryKey;autoIncrement:false"`
		Name string `gorm:"size:64"`
	}
	db, _, pool := openOracleWithFakeDB(t)
	db = db.Table(oracleReviewTable)
	pool.server.reply(oracleCurrentSchemaSQL, "U")
	pool.server.reply(oracleHasTableSQL, "1")
	pool.server.reply(oracleHasConstraintSQL, "0")
	pool.server.replyRows(oracleColumnTypesSQL, testOracleColumnNames, [][]driver.Value{
		{"ID", nil, int64(0), testOracleNumber, int64(0), int64(19), int64(0)},
		{testPhysicalName, nil, int64(1), testOracleVarchar2, int64(64), nil, nil},
	})
	if err := db.AutoMigrate(&unique{}); err != nil {
		t.Fatal(err)
	}
	wantAdd := `ALTER TABLE "REVIEW_ROWS" ADD CONSTRAINT "UNI_REVIEW_ROWS_NAME" UNIQUE ("NAME")`
	if got := pool.server.recordedExecs(); !reflect.DeepEqual(got, []string{wantAdd}) {
		t.Fatalf("add unique: %v", got)
	}
	pool.server.reply(oracleHasConstraintSQL, "1")
	if err := db.AutoMigrate(&unique{}); err != nil {
		t.Fatal(err)
	}
	if got := pool.server.recordedExecs(); len(got) != 1 {
		t.Fatalf("repeated migration: %v", got)
	}
	if err := db.AutoMigrate(&ordinary{}); err != nil {
		t.Fatal(err)
	}
	wantDrop := `ALTER TABLE "REVIEW_ROWS" DROP CONSTRAINT "UNI_REVIEW_ROWS_NAME"`
	if got := pool.server.recordedExecs(); !reflect.DeepEqual(got, []string{wantAdd, wantDrop}) {
		t.Fatalf("drop unique: %v", got)
	}
	pool.server.reply(oracleHasConstraintSQL, "0")
	if err := db.AutoMigrate(&ordinary{}); err != nil {
		t.Fatal(err)
	}
	if got := pool.server.recordedExecs(); len(got) != 2 {
		t.Fatalf("ordinary migration: %v", got)
	}
}

func TestOracleUniqueMigrationPropagatesDictionaryError(t *testing.T) {
	type row struct {
		Name string `gorm:"unique"`
	}
	db, _, pool := openOracleWithFakeDB(t)
	pool.server.reply(oracleCurrentSchemaSQL, "U")
	want := errors.New("constraint dictionary unavailable")
	pool.server.replyError(oracleHasConstraintSQL, want)
	stmt := &gorm.Statement{DB: db}
	if err := stmt.Parse(&row{}); err != nil {
		t.Fatal(err)
	}
	err := db.Migrator().MigrateColumnUnique(&row{}, stmt.Schema.FieldsByDBName["name"], migrator.ColumnType{})
	if !errors.Is(err, want) || len(pool.server.recordedExecs()) != 0 {
		t.Fatalf("err=%v execs=%v", err, pool.server.recordedExecs())
	}
}

func TestOracleMigrateNumberScale(t *testing.T) {
	type inferred struct {
		Amount float64 `gorm:"precision:12;scale:4"`
	}
	type explicit struct {
		Amount float64 `gorm:"type:NUMBER(12,4)"`
	}
	type whole struct {
		Amount float64 `gorm:"type:NUMBER(12)"`
	}
	type negative struct {
		Amount float64 `gorm:"type:NUMBER(12,-2)"`
	}
	type unconstrained struct {
		Amount float64 `gorm:"type:NUMBER"`
	}
	for _, tc := range []struct {
		name             string
		model            interface{}
		precision, scale int64
		wantAlter        bool
		unconstrained    bool
	}{
		{"inferred_scale", &inferred{}, 12, 2, true, false},
		{"explicit_scale", &explicit{}, 12, 2, true, false},
		{"explicit_precision", &explicit{}, 10, 4, true, false},
		{"whole_scale", &whole{}, 12, 2, true, false},
		{"negative_scale", &negative{}, 12, 2, true, false},
		{"unchanged", &inferred{}, 12, 4, false, false},
		{"unchanged_explicit", &explicit{}, 12, 4, false, false},
		{"unchanged_whole", &whole{}, 12, 0, false, false},
		{"unchanged_negative", &negative{}, 12, -2, false, false},
		{"add_precision", &explicit{}, 0, 0, true, true},
		{"remove_precision", &unconstrained{}, 12, 4, true, false},
		{"unchanged_unconstrained", &unconstrained{}, 0, 0, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, _, pool := openOracleWithFakeDB(t)
			pool.server.reply(oracleCurrentSchemaSQL, "U")
			pool.server.reply(oracleHasConstraintSQL, "0")
			var precision, scale driver.Value = tc.precision, tc.scale
			if tc.unconstrained {
				precision, scale = nil, nil
			}
			pool.server.replyRows(oracleColumnTypesSQL, testOracleColumnNames, [][]driver.Value{{"AMOUNT", nil, int64(1), testOracleNumber, int64(0), precision, scale}})
			stmt := &gorm.Statement{DB: db}
			if err := stmt.Parse(tc.model); err != nil {
				t.Fatal(err)
			}
			column := migrator.ColumnType{
				NameValue: sql.NullString{String: "amount", Valid: true}, DataTypeValue: sql.NullString{String: testOracleNumber, Valid: true},
				DecimalSizeValue: sql.NullInt64{Int64: tc.precision, Valid: !tc.unconstrained}, ScaleValue: sql.NullInt64{Int64: tc.scale, Valid: !tc.unconstrained}, NullableValue: sql.NullBool{Bool: true, Valid: true},
			}
			if err := db.Migrator().MigrateColumn(tc.model, stmt.Schema.FieldsByDBName["amount"], column); err != nil {
				t.Fatal(err)
			}
			wantCount := 0
			if tc.wantAlter {
				wantCount = 1
			}
			if got := pool.server.recordedExecs(); len(got) != wantCount {
				t.Fatalf("execs=%v want %d ALTER", got, wantCount)
			}
		})
	}
}

func TestOracleAlterClearsColumnProperties(t *testing.T) {
	type row struct {
		Name string `gorm:"size:64"`
	}
	for _, tc := range []struct {
		name         string
		nullable     int64
		defaultValue driver.Value
		suffix       string
	}{
		{"not_null", 0, nil, " NULL"},
		{"default", 1, oracleOldDefault, " DEFAULT NULL"},
		{"both", 0, oracleOldDefault, " DEFAULT NULL NULL"},
		{"already_nullable", 1, nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, _, pool := openOracleWithFakeDB(t)
			pool.server.reply(oracleCurrentSchemaSQL, "U")
			pool.server.replyRows(oracleColumnTypesSQL, testOracleColumnNames, [][]driver.Value{{testPhysicalName, tc.defaultValue, tc.nullable, testOracleVarchar2, int64(64), nil, nil}})
			if err := db.Migrator().AlterColumn(&row{}, "Name"); err != nil {
				t.Fatal(err)
			}
			want := `ALTER TABLE "ROWS" MODIFY ("NAME" VARCHAR2(64)` + tc.suffix + `)`
			if got := pool.server.recordedExecs(); !reflect.DeepEqual(got, []string{want}) {
				t.Fatalf("execs=%v want %s", got, want)
			}
		})
	}
}

func TestOracleAlterPreservesIdentityAndNotNull(t *testing.T) {
	type row struct {
		ID     uint
		Serial uint   `gorm:"autoIncrement"`
		Name   string `gorm:"size:64;not null;default:kept"`
	}
	db, _, pool := openOracleWithFakeDB(t)
	pool.server.reply(oracleCurrentSchemaSQL, "U")
	pool.server.replyRows(oracleColumnTypesSQL, testOracleColumnNames, [][]driver.Value{
		{"ID", `"U"."ISEQ$$123".nextval`, int64(0), testOracleNumber, int64(0), int64(20), int64(0)},
		{"SERIAL", `"U"."ISEQ$$456".nextval`, int64(0), testOracleNumber, int64(0), int64(20), int64(0)},
		{testPhysicalName, "'kept'", int64(0), testOracleVarchar2, int64(64), nil, nil},
	})
	if err := db.Migrator().AlterColumn(&row{}, "ID"); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrator().AlterColumn(&row{}, "Serial"); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrator().AlterColumn(&row{}, "Name"); err != nil {
		t.Fatal(err)
	}
	for _, query := range pool.server.recordedExecs() {
		if strings.Contains(query, "NULL") || strings.Contains(query, "IDENTITY") {
			t.Fatalf("reset existing property: %s", query)
		}
	}
}

func TestOracleColumnNullDefaultIsAbsent(t *testing.T) {
	type row struct {
		Name string `gorm:"size:64"`
	}
	db, _, pool := openOracleWithFakeDB(t)
	pool.server.reply(oracleCurrentSchemaSQL, "U")
	pool.server.reply(oracleHasTableSQL, "1")
	pool.server.reply(oracleHasConstraintSQL, "0")
	pool.server.replyRows(oracleColumnTypesSQL, testOracleColumnNames, [][]driver.Value{{testPhysicalName, " NULL ", int64(1), testOracleVarchar2, int64(64), nil, nil}})
	if err := db.AutoMigrate(&row{}); err != nil {
		t.Fatal(err)
	}
	if got := pool.server.recordedExecs(); len(got) != 0 {
		t.Fatalf("NULL default causes repeated DDL: %v", got)
	}
}

func TestOracleMigrateColumnDryRun(t *testing.T) {
	type row struct {
		Name string `gorm:"size:64;unique"`
	}
	db, _, pool := openOracleWithFakeDB(t)
	pool.server.reply(oracleCurrentSchemaSQL, "U")
	pool.server.reply(oracleHasTableSQL, "1")
	pool.server.reply(oracleHasConstraintSQL, "0")
	pool.server.replyRows(oracleColumnTypesSQL, testOracleColumnNames, [][]driver.Value{
		{testPhysicalName, oracleOldDefault, int64(0), testOracleVarchar2, int64(64), nil, nil},
	})
	if err := db.Session(&gorm.Session{DryRun: true}).Table(oracleReviewTable).AutoMigrate(&row{}); err != nil {
		t.Fatal(err)
	}
	if got := pool.server.recordedExecs(); len(got) != 0 {
		t.Fatalf("DryRun executed DDL: %v", got)
	}
}

func TestOracleMigrationQualifiedSchema(t *testing.T) {
	for _, tc := range []struct{ name, owner, table string }{
		{"other.rows", "OTHER", "ROWS"},
		{`"Other"."Rows"`, "Other", oracleQuotedRows},
		{`"Other.Space"."Rows"`, "Other.Space", oracleQuotedRows},
		{`"Ow""ner"."Rows"`, `Ow"ner`, oracleQuotedRows},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, _, pool := openOracleWithFakeDB(t)
			pool.server.reply(oracleCurrentSchemaSQL, "CURRENT_USER")
			pool.server.reply(oracleHasTableSQL, "1")
			m := db.Table(tc.name).Migrator().(*oracleMigrator)
			if !m.HasTable(&oracleMigrationRow{}) {
				t.Fatal("table not found")
			}
			if err := m.RunWithValue(&oracleMigrationRow{}, func(stmt *gorm.Statement) error {
				owner, table := m.CurrentSchema(stmt, stmt.Table)
				if owner != tc.owner || oracleIdentifierName(table) != tc.table {
					t.Fatalf("lookup=%q.%q want %q.%q", owner, table, tc.owner, tc.table)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
