package oceanbase

import (
	"context"
	"database/sql/driver"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const testCustomPhysicalColumn = "CUSTOMVALUE"

type oracleMigrationRow struct {
	ID   uint
	Name string `gorm:"size:64;index"`
	Age  int
}

func TestOracleAutoMigrateExistingColumns(t *testing.T) {
	db, _, pool := openOracleWithFakeDB(t)
	pool.server.reply(oracleCurrentSchemaSQL, "U")
	pool.server.reply(oracleHasTableSQL, "1")
	pool.server.reply(oracleHasConstraintSQL, "0")
	pool.server.replyRows(oracleColumnTypesSQL,
		testOracleColumnNames,
		[][]driver.Value{
			{"ID", nil, int64(0), testOracleNumber, int64(0), int64(20), int64(0)},
			{testPhysicalName, nil, int64(1), testOracleVarchar2, int64(64), nil, nil},
			{"AGE", nil, int64(1), testOracleNumber, int64(0), int64(19), int64(0)},
		})
	pool.server.reply("SELECT count(*) FROM all_indexes WHERE table_owner = ? AND table_name = ? AND index_name = ?", "1")
	if err := db.AutoMigrate(&oracleMigrationRow{}); err != nil {
		t.Fatal(err)
	}
	if got := pool.server.recordedExecs(); len(got) != 0 {
		t.Fatalf("unchanged schema executed DDL: %v", got)
	}
}

func TestOracleIndexAndAlterSQL(t *testing.T) {
	db, _, pool := openOracleWithFakeDB(t)
	pool.server.reply(oracleCurrentSchemaSQL, "U")
	pool.server.replyRows(oracleColumnTypesSQL, testOracleColumnNames, [][]driver.Value{
		{testPhysicalName, nil, int64(1), testOracleVarchar2, int64(64), nil, nil},
	})
	if err := db.Migrator().CreateTable(&oracleMigrationRow{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrator().AlterColumn(&oracleMigrationRow{}, "Name"); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrator().DropIndex(&oracleMigrationRow{}, "Name"); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrator().RenameIndex(&oracleMigrationRow{}, "old_idx", "new_idx"); err != nil {
		t.Fatal(err)
	}
	execs := pool.server.recordedExecs()
	if len(execs) != 5 || strings.Contains(execs[0], "INDEX") {
		t.Fatalf("execs=%v", execs)
	}
	for _, want := range []string{
		`CREATE INDEX "IDX_ORACLE_MIGRATION_ROWS_NAME" ON "ORACLE_MIGRATION_ROWS"("NAME")`,
		`ALTER TABLE "ORACLE_MIGRATION_ROWS" MODIFY ("NAME" VARCHAR2(64))`,
		`DROP INDEX "IDX_ORACLE_MIGRATION_ROWS_NAME"`,
		`ALTER INDEX "OLD_IDX" RENAME TO "NEW_IDX"`,
	} {
		if !containsExec(execs, want) {
			t.Errorf("missing %s in %v", want, execs)
		}
	}
}

func TestOracleCustomColumnNamesScan(t *testing.T) {
	type row struct {
		ID     uint
		Custom string `gorm:"column:customValue"`
		Quoted string `gorm:"column:\"Foo\""`
	}
	for _, scan := range []bool{false, true} {
		db, _, pool := openOracleWithFakeDB(t)
		var rows []row
		dry := db.Session(&gorm.Session{DryRun: true}).Model(&row{}).Find(&rows)
		pool.server.replyRows(dry.Statement.SQL.String(), []string{"ID", testCustomPhysicalColumn, "Foo"}, [][]driver.Value{{int64(1), "custom-value", "quoted"}})
		var tx *gorm.DB
		if scan {
			tx = db.Model(&row{}).Scan(&rows)
		} else {
			tx = db.Find(&rows)
		}
		if tx.Error != nil || len(rows) != 1 || rows[0].Custom != "custom-value" || rows[0].Quoted != "quoted" {
			t.Fatalf("scan=%v err=%v rows=%+v", scan, tx.Error, rows)
		}
	}
}

func TestOraclePrecisionZero(t *testing.T) {
	type row struct {
		Stamp time.Time `gorm:"precision:0"`
	}
	db, _, _ := openOracleWithFakeDB(t)
	stmt := &gorm.Statement{DB: db}
	if err := stmt.Parse(&row{}); err != nil {
		t.Fatal(err)
	}
	if got := db.DataTypeOf(stmt.Schema.FieldsByDBName["stamp"]); got != "TIMESTAMP(0)" {
		t.Fatalf("type=%s", got)
	}
}

type oracleScanChild struct {
	ID    uint
	Value string `gorm:"column:customValue"`
}

type oracleScanParent struct {
	ID      uint
	ChildID uint
	Child   oracleScanChild `gorm:"foreignKey:ChildID"`
}

func TestOracleJoinedCustomNamesScan(t *testing.T) {
	db, _, pool := openOracleWithFakeDB(t)
	var rows []oracleScanParent
	dry := db.Session(&gorm.Session{DryRun: true}).Joins("Child").Find(&rows)
	pool.server.replyRows(dry.Statement.SQL.String(), []string{"ID", "CHILD_ID", "CHILD__ID", "CHILD__CUSTOMVALUE"}, [][]driver.Value{{int64(1), int64(2), int64(2), "joined"}})
	if err := db.Joins("Child").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Child.Value != "joined" {
		t.Fatalf("rows=%+v", rows)
	}
}

func TestOracleUserColumnMappingWins(t *testing.T) {
	db, _, pool := openOracleWithFakeDB(t)
	mapping := map[string]string{testCustomPhysicalColumn: "id"}
	pool.server.replyRows(`SELECT * FROM "ORACLE_SCAN_CHILDREN"`, []string{testCustomPhysicalColumn}, [][]driver.Value{{int64(9)}})
	var row oracleScanChild
	if err := db.MapColumns(mapping).Find(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.ID != 9 || len(mapping) != 1 {
		t.Fatalf("row=%+v mapping=%v", row, mapping)
	}
}

func TestOracleExplicitJoinAliasScan(t *testing.T) {
	db, _, pool := openOracleWithFakeDB(t)
	query := gorm.G[oracleScanParent](db).Joins(clause.JoinTarget{Association: "Child"}.As("x"), nil)
	stmt := &gorm.Statement{DB: db}
	query.Build(stmt)
	pool.server.replyRows(stmt.SQL.String(), []string{"ID", "CHILD_ID", "X__ID", "X__CUSTOMVALUE"}, [][]driver.Value{{int64(1), int64(2), int64(2), "aliased"}})
	rows, err := query.Find(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Child.ID != 2 || rows[0].Child.Value != "aliased" {
		t.Fatalf("rows=%+v", rows)
	}
}
