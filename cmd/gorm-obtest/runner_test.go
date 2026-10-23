package main

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	ob "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/dberrors"
	oceanbase "github.com/cocosip/gorm-oceanbase"
	"gorm.io/gorm"
)

type statementCounter struct{ opened, closed int }
type statementConnector struct{ counter *statementCounter }

func (c statementConnector) Driver() driver.Driver { return nil }
func (c statementConnector) Connect(context.Context) (driver.Conn, error) {
	return statementConnection(c), nil
}

type statementConnection struct{ counter *statementCounter }

func (c statementConnection) Prepare(string) (driver.Stmt, error) {
	c.counter.opened++
	return countedStatement(c), nil
}
func (statementConnection) Close() error              { return nil }
func (statementConnection) Begin() (driver.Tx, error) { return nil, errors.New("transaction not used") }

type countedStatement struct{ counter *statementCounter }

func (s countedStatement) Close() error { s.counter.closed++; return nil }
func (countedStatement) NumInput() int  { return -1 }
func (countedStatement) Exec([]driver.Value) (driver.Result, error) {
	return driver.RowsAffected(1), nil
}
func (countedStatement) Query([]driver.Value) (driver.Rows, error) {
	return nil, errors.New("query not used")
}

func TestPreparedStatementsReleasedBeforeAndAfterCleanup(t *testing.T) {
	counter := &statementCounter{}
	pool := sql.OpenDB(statementConnector{counter})
	defer func() { _ = pool.Close() }()
	prepared := gorm.NewPreparedStmtDB(pool, 0, 0)
	base := &gorm.DB{Config: &gorm.Config{ConnPool: prepared}}
	base.Statement = &gorm.Statement{DB: base, ConnPool: prepared, Context: context.Background()}
	s := &scenario{base: base, cleanupTimeout: time.Second}
	s.cleanups = append(s.cleanups, cleanupAction{name: "test", run: func(*gorm.DB) error {
		if len(prepared.Stmts.Keys()) != 0 {
			return checkFailure("previous case statements retained during cleanup")
		}
		_, err := prepared.ExecContext(context.Background(), "DROP TABLE owned")
		return err
	}})
	result := runScenario(s, func(*scenario) error {
		_, err := prepared.ExecContext(context.Background(), "INSERT INTO owned VALUES (1)")
		return err
	})
	if result.Status != statusPass || counter.opened != 2 || counter.closed != 2 || len(prepared.Stmts.Keys()) != 0 {
		t.Fatalf("result=%+v opened=%d closed=%d", result, counter.opened, counter.closed)
	}
}

type metadataFailurePool struct{ gorm.ConnPool }

func (metadataFailurePool) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	return nil, context.DeadlineExceeded
}

func TestObjectAbsenceCannotHideMetadataFailure(t *testing.T) {
	for _, mode := range []string{modeMySQL, modeOracle} {
		m := ob.ModeMySQL
		if mode == modeOracle {
			m = ob.ModeOracle
		}
		db, err := gorm.Open(oceanbase.New(oceanbase.Config{Conn: metadataFailurePool{}, Mode: m, SkipInitializeWithVersion: true}), &gorm.Config{DisableAutomaticPing: true})
		if err != nil {
			t.Fatal(err)
		}
		s := &scenario{db: db, mode: mode}
		for _, kind := range []string{"index", "column", "constraint", "table"} {
			if err := s.assertAbsent(kind, "owned", "removed"); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("mode=%s kind=%s error=%v", mode, kind, err)
			}
		}
	}
}

func TestExpectedConstraintErrorRejectsUnrelatedFailure(t *testing.T) {
	for _, err := range []error{nil, context.DeadlineExceeded, errors.New("connection failed"), &dberrors.Error{Code: 9999}} {
		if constraintError(err, modeOracle, 1048, 1400) == nil {
			t.Fatalf("accepted unrelated failure %T", err)
		}
	}
	for _, tc := range []struct {
		mode string
		code uint32
	}{{modeMySQL, 1048}, {modeOracle, 1400}} {
		if err := constraintError(&dberrors.Error{Code: tc.code}, tc.mode, 1048, 1400); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOfflineCommandsNeverReadDSNs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"targets":[{"name":"oracle","mode":"oracle","dsn_env":"GORMAUDIT_NO_DSN"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GORMAUDIT_NO_DSN", "")
	for _, args := range [][]string{{"-list"}, {"-config", path, "-check"}} {
		var output, stderr bytes.Buffer
		if got := command(args, &output, &stderr); got != 0 {
			t.Fatalf("args=%v exit=%d err=%s", args, got, stderr.String())
		}
	}
	var output, stderr bytes.Buffer
	if got := command([]string{"-config", path}, &output, &stderr); got != 2 {
		t.Fatalf("missing DSN exit=%d", got)
	}
}

func TestFailuresDoNotExposeDSNOrRawErrors(t *testing.T) {
	want := "oceanbase://secret:password@private-host/db"
	if got := errorSummary(errors.New(want)); strings.Contains(got, want) || strings.Contains(got, "password") {
		t.Fatalf("leaked %s", got)
	}
	cfg := configuration{Targets: []targetConfig{{Name: "test", Mode: modeOracle, DSNEnv: "GORMAUDIT_SECRET"}}}
	t.Setenv("GORMAUDIT_SECRET", want+"?mode=mysql")
	_, err := connectionConfigs(cfg)
	if err == nil || strings.Contains(err.Error(), "password") {
		t.Fatalf("err=%v", err)
	}
}

func TestCleanupContinuesAfterErrorsAndPanic(t *testing.T) {
	for _, panicCase := range []bool{false, true} {
		var order []int
		base := &gorm.DB{Config: &gorm.Config{}}
		base.Statement = &gorm.Statement{DB: base, Context: context.Background()}
		s := &scenario{base: base, cleanupTimeout: time.Second}
		for i := 0; i < 3; i++ {
			s.cleanups = append(s.cleanups, cleanupAction{name: "owned", run: func(db *gorm.DB) error {
				if db.Statement.Context.Err() != nil {
					return checkFailure("cleanup inherited canceled context")
				}
				order = append(order, i)
				if i == 1 {
					if panicCase {
						panic("cleanup secret must not leak")
					}
					return errors.New("cleanup failure")
				}
				return nil
			}})
		}
		result := runScenario(s, func(*scenario) error {
			if panicCase {
				panic("must not leak")
			}
			return context.Canceled
		})
		if result.Status != statusFail || len(result.CleanupErrors) != 1 || len(order) != 3 || order[0] != 2 || order[2] != 0 {
			t.Fatalf("result=%+v order=%v", result, order)
		}
	}
}
