package oceanbase

import (
	"errors"
	"strings"
	"testing"

	ob "github.com/cocosip/go-oceanbase"
	"gorm.io/gorm"
)

type mysqlInsertResult struct {
	id  int64
	err error
}

func (r mysqlInsertResult) LastInsertId() (int64, error) { return r.id, r.err }
func (mysqlInsertResult) RowsAffected() (int64, error)   { return 1, nil }

func TestMySQLCreateResultContract(t *testing.T) {
	overflow := errors.New("insert ID overflow")
	for _, tc := range []struct {
		name    string
		result  mysqlInsertResult
		wantErr error
		wantID  uint
	}{
		{name: "absent", result: mysqlInsertResult{err: ob.ErrUnsupported}},
		{name: "generated", result: mysqlInsertResult{id: 42}, wantID: 42},
		{name: "overflow", result: mysqlInsertResult{err: overflow}, wantErr: overflow},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := newFakePool(t)
			pool.server.execResult = tc.result
			db, err := gorm.Open(New(Config{Conn: pool, Mode: ob.ModeMySQL, SkipInitializeWithVersion: true}), &gorm.Config{DisableAutomaticPing: true})
			if err != nil {
				t.Fatal(err)
			}
			type row struct {
				ID   uint
				Name string
			}
			r := row{Name: testUserName}
			tx := db.Create(&r)
			if !errors.Is(tx.Error, tc.wantErr) || r.ID != tc.wantID {
				t.Fatalf("err=%v ID=%d", tx.Error, r.ID)
			}
			wantTx := "COMMIT"
			if tc.wantErr != nil {
				wantTx = "ROLLBACK"
			}
			if got := pool.server.recordedTx(); len(got) != 2 || got[1] != wantTx {
				t.Fatalf("transactions=%v", got)
			}
			if tc.name == "absent" {
				if err := db.Table("plain_rows").Create(map[string]interface{}{"payload": testUserName}).Error; err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestMySQLDefaultOnlyBatchRetainsEveryTuple(t *testing.T) {
	type key struct{ ID uint }
	for _, count := range []int{1, 3} {
		db := openWithFakeDB(t)
		rows := make([]key, count)
		tx := db.Session(&gorm.Session{DryRun: true}).Create(&rows)
		if tx.Error != nil {
			t.Fatal(tx.Error)
		}
		if got := strings.Count(tx.Statement.SQL.String(), "()"); got != count {
			t.Fatalf("rows=%d tuples=%d SQL=%s", count, got, tx.Statement.SQL.String())
		}
	}
}

func TestMySQLDropTableRestoresForeignKeyChecks(t *testing.T) {
	ddlErr := errors.New("drop failed")
	for _, original := range []string{"0", "1"} {
		t.Run(original, func(t *testing.T) {
			pool := newFakePool(t)
			pool.server.reply("SELECT @@FOREIGN_KEY_CHECKS", original)
			pool.server.replyExecError("DROP TABLE IF EXISTS `drop_rows` CASCADE", ddlErr)
			db, err := gorm.Open(New(Config{Conn: pool.db, Mode: ob.ModeMySQL, SkipInitializeWithVersion: true}), &gorm.Config{DisableAutomaticPing: true})
			if err != nil {
				t.Fatal(err)
			}
			if err := db.Migrator().DropTable("drop_rows"); !errors.Is(err, ddlErr) {
				t.Fatalf("err=%v", err)
			}
			execs := pool.server.recordedExecs()
			if len(execs) < 3 || execs[len(execs)-1] != "SET FOREIGN_KEY_CHECKS = "+original {
				t.Fatalf("execs=%v", execs)
			}
		})
	}
}

func TestMySQLUniqueIndexStringGetsBoundedType(t *testing.T) {
	type row struct {
		Code string `gorm:"uniqueIndex"`
	}
	db := openWithFakeDB(t)
	stmt := &gorm.Statement{DB: db}
	if err := stmt.Parse(&row{}); err != nil {
		t.Fatal(err)
	}
	if got := db.DataTypeOf(stmt.Schema.FieldsByDBName["code"]); !strings.HasPrefix(got, "varchar(") {
		t.Fatalf("type=%s", got)
	}
}

func TestMySQLDropTableDisableAndRestoreErrors(t *testing.T) {
	for _, disable := range []bool{true, false} {
		pool := newFakePool(t)
		pool.server.reply("SELECT @@FOREIGN_KEY_CHECKS", "1")
		want := errors.New("session setting failed")
		query := "SET FOREIGN_KEY_CHECKS = 1"
		if disable {
			query = "SET FOREIGN_KEY_CHECKS = 0"
		}
		pool.server.replyExecError(query, want)
		db, err := gorm.Open(New(Config{Conn: pool.db, Mode: ob.ModeMySQL, SkipInitializeWithVersion: true}), &gorm.Config{DisableAutomaticPing: true})
		if err != nil {
			t.Fatal(err)
		}
		if err := db.Migrator().DropTable("drop_rows"); !errors.Is(err, want) {
			t.Fatalf("err=%v", err)
		}
		if disable && containsExec(pool.server.recordedExecs(), "DROP TABLE") {
			t.Fatal("disabled checks failed but DDL executed")
		}
		if !disable && pool.db.Stats().Idle != 0 {
			t.Fatal("connection with unrestored checks was returned to pool")
		}
	}
}
