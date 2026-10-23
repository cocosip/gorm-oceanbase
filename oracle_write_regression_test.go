package oceanbase

import (
	"database/sql/driver"
	"errors"
	"strings"
	"testing"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	testUniqueIndex      = "UNI"
	testOracleColumnName = "COLUMN_NAME"
	testOracleNumber     = "NUMBER"
	testPhysicalCode     = "CODE"
	testConflictRegion   = "region"
	testConflictCode     = "code"
)

var testOracleIndexColumns = []string{"INDEX_NAME", "UNIQUE_FLAG", "CONSTRAINT_TYPE", testOracleColumnName}

func TestOracleDefaultOnlyBatchPreservesRowsAndKeys(t *testing.T) {
	type keyOnly struct{ ID uint }
	db, _, pool := openOracleWithFakeDB(t)
	pool.server.seedOut(901)
	rows := []keyOnly{{}, {}, {ID: 8}, {}}
	tx := db.Create(&rows)
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	if rows[0].ID != 901 || rows[1].ID != 902 || rows[2].ID != 8 || rows[3].ID != 903 || tx.RowsAffected != 4 {
		t.Fatalf("rows=%v affected=%d execs=%v", rows, tx.RowsAffected, pool.server.recordedExecs())
	}
	all := []keyOnly{{}, {}}
	if err := db.Create(&all).Error; err != nil {
		t.Fatal(err)
	}
	if all[0].ID != 904 || all[1].ID != 905 {
		t.Fatalf("all default keys=%v", all)
	}
}

func TestOracleDefaultOnlyUpsert(t *testing.T) {
	type keyOnly struct{ ID uint }
	db, _, pool := openOracleWithFakeDB(t)
	pool.server.seedOut(11)
	rows := []keyOnly{{}, {}}
	if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if rows[0].ID != 11 || rows[1].ID != 12 {
		t.Fatalf("rows=%v", rows)
	}
}

func TestOracleEmptySchemalessMapReturnsError(t *testing.T) {
	db, _, pool := openOracleWithFakeDB(t)
	if err := db.Table("plain_rows").Create(map[string]interface{}{}).Error; !errors.Is(err, gorm.ErrInvalidData) {
		t.Fatalf("err=%v", err)
	}
	if len(pool.server.recordedExecs()) != 0 {
		t.Fatal("invalid row executed DML")
	}
}

type oracleUniqueRow struct {
	ID     uint
	Code   string `gorm:"uniqueIndex:uni_code_region"`
	Region string `gorm:"uniqueIndex:uni_code_region"`
	Name   string
}

func TestOracleUpsertUniqueTargetValidation(t *testing.T) {
	for _, tc := range []struct {
		name      string
		cols      []clause.Column
		indexRows [][]driver.Value
		wantErr   bool
	}{
		{"composite", []clause.Column{{Name: testConflictRegion}, {Name: testConflictCode}}, [][]driver.Value{{testUniqueIndex, int64(1), nil, testPhysicalCode}, {testUniqueIndex, int64(1), nil, "REGION"}}, false},
		{"partial", []clause.Column{{Name: testConflictCode}}, [][]driver.Value{{testUniqueIndex, int64(1), nil, testPhysicalCode}, {testUniqueIndex, int64(1), nil, "REGION"}}, true},
		{"nonunique", []clause.Column{{Name: testConflictCode}}, [][]driver.Value{{"IDX", int64(0), nil, testPhysicalCode}}, true},
		{"duplicate", []clause.Column{{Name: testConflictCode}, {Name: testPhysicalCode}}, [][]driver.Value{{testUniqueIndex, int64(1), nil, testPhysicalCode}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, _, pool := openOracleWithFakeDB(t)
			pool.server.reply(oracleCurrentSchemaSQL, "U")
			pool.server.replyRows(oracleIndexSQL, testOracleIndexColumns, tc.indexRows)
			err := db.Clauses(clause.OnConflict{Columns: tc.cols, DoNothing: true}).Create(&oracleUniqueRow{ID: 1, Code: "c", Region: "r"}).Error
			if tc.wantErr {
				if !errors.Is(err, ErrUpsertUnsupported) || len(pool.server.recordedExecs()) != 0 {
					t.Fatalf("err=%v execs=%v", err, pool.server.recordedExecs())
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestOracleNonPKUpsertRejectsMissingGeneratedKeyBeforeBatch(t *testing.T) {
	db, _, pool := openOracleWithFakeDB(t)
	rows := []oracleUniqueRow{{ID: 1, Code: "a", Region: "r"}, {Code: "b", Region: "r"}}
	err := db.Session(&gorm.Session{DryRun: true}).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: testConflictCode}, {Name: testConflictRegion}}, DoNothing: true}).Create(&rows).Error
	if !errors.Is(err, ErrUpsertUnsupported) || len(pool.server.recordedExecs()) != 0 {
		t.Fatalf("err=%v", err)
	}
}

func TestOracleUpdateAllExcludesOnlyImplicitONAssignments(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		db, _, _ := openOracleWithFakeDB(t)
		oc := clause.OnConflict{Columns: []clause.Column{{Name: testConflictCode}, {Name: testConflictRegion}}, UpdateAll: true}
		if explicit {
			oc.DoUpdates = clause.AssignmentColumns([]string{testPhysicalCode})
		}
		tx := db.Session(&gorm.Session{DryRun: true}).Clauses(oc).Create(&oracleUniqueRow{ID: 1, Code: "c", Region: "r", Name: "new"})
		if explicit {
			if !errors.Is(tx.Error, ErrUpsertUnsupported) {
				t.Fatalf("explicit err=%v", tx.Error)
			}
			continue
		}
		if tx.Error != nil {
			t.Fatal(tx.Error)
		}
		sql := tx.Statement.SQL.String()
		if strings.Contains(sql, `SET "ORACLE_UNIQUE_ROWS".testPhysicalCode`) || !strings.Contains(sql, `SET "ORACLE_UNIQUE_ROWS"."NAME"`) {
			t.Fatalf("sql=%s", sql)
		}
	}
}

func TestOracleUpsertDictionaryErrorStopsDML(t *testing.T) {
	db, _, pool := openOracleWithFakeDB(t)
	pool.server.reply(oracleCurrentSchemaSQL, "U")
	want := errors.New("dictionary denied")
	pool.server.replyError(oracleIndexSQL, want)
	err := db.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: testConflictCode}}, DoNothing: true}).Create(&oracleUniqueRow{ID: 1}).Error
	if !errors.Is(err, want) || len(pool.server.recordedExecs()) != 0 {
		t.Fatalf("err=%v execs=%v", err, pool.server.recordedExecs())
	}
}

func TestOracleUpsertQuotedUniqueTarget(t *testing.T) {
	type row struct {
		ID   uint
		Code string `gorm:"column:\"Mixed\";unique"`
	}
	db, _, pool := openOracleWithFakeDB(t)
	pool.server.reply(oracleCurrentSchemaSQL, "U")
	pool.server.replyRows(oracleIndexSQL, testOracleIndexColumns, [][]driver.Value{{"UNI_MIXED", int64(1), nil, "Mixed"}})
	if err := db.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: `"Mixed"`}}, DoNothing: true}).Create(&row{ID: 1, Code: "a"}).Error; err != nil {
		t.Fatal(err)
	}
}
