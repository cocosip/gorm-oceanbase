package oceanbase

import (
	"errors"
	"strings"
	"testing"
	"time"

	ob "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/dberrors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// dialectorWithMode builds an initialized dialector for SQL generation tests.
func dialectorWithMode(t *testing.T, mode ob.Mode) *Dialector {
	t.Helper()
	d := &Dialector{Config: &Config{resolvedMode: mode}}
	return d
}

// openWithFakeDB boots a gorm.DB against the in-process fake server.
func openWithFakeDB(t *testing.T) *gorm.DB {
	t.Helper()
	pool := newFakePool(t)
	pool.server.reply("SELECT OB_VERSION()", testOBCEVersion)
	dialector := &Dialector{Config: &Config{Conn: pool, Mode: ob.ModeMySQL}}
	db, err := gorm.Open(dialector, &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatalf("gorm.Open: %v", err)
	}
	return db
}

func TestInitializeRejectsMissingConnectionSource(t *testing.T) {
	d := &Dialector{Config: &Config{}}
	if _, err := gorm.Open(d, &gorm.Config{DisableAutomaticPing: true}); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("err = %v, want ErrInvalidConfiguration", err)
	}
}

func TestInitializeRejectsMultipleConnectionSources(t *testing.T) {
	d := &Dialector{Config: &Config{DSN: testDSNModeMySQL, DSNConfig: &ob.Config{}}}
	if _, err := gorm.Open(d, &gorm.Config{DisableAutomaticPing: true}); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("err = %v, want ErrInvalidConfiguration", err)
	}
}

func TestInitializeRequiresConcreteModeForExternalConn(t *testing.T) {
	pool := newFakePool(t)
	d := &Dialector{Config: &Config{Conn: pool}}
	if _, err := gorm.Open(d, &gorm.Config{DisableAutomaticPing: true}); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("err = %v, want ErrInvalidConfiguration", err)
	}
}

func TestInitializeVersionFallbackOnlyForMissingFunction(t *testing.T) {
	pool := newFakePool(t)
	pool.server.replyError("SELECT OB_VERSION()", &dberrors.Error{Code: 1305, Message: "FUNCTION does not exist"})
	pool.server.reply("SELECT VERSION()", testOBCEVersion)

	d := &Dialector{Config: &Config{Conn: pool, Mode: ob.ModeMySQL}}
	if _, err := gorm.Open(d, &gorm.Config{DisableAutomaticPing: true}); err != nil {
		t.Fatalf("gorm.Open: %v", err)
	}
	if !containsQuery(pool.server.recordedQueries(), "SELECT VERSION()") {
		t.Fatalf("expected VERSION() fallback, queries: %v", pool.server.recordedQueries())
	}
	if d.ServerVersion != testOBCEVersion {
		t.Fatalf("ServerVersion = %q", d.ServerVersion)
	}
}

func TestInitializePropagatesNonFunctionVersionError(t *testing.T) {
	pool := newFakePool(t)
	pool.server.replyError("SELECT OB_VERSION()", errors.New("network down"))

	d := &Dialector{Config: &Config{Conn: pool, Mode: ob.ModeMySQL}}
	if _, err := gorm.Open(d, &gorm.Config{DisableAutomaticPing: true}); err == nil {
		t.Fatal("expected initialization error")
	}
	if containsQuery(pool.server.recordedQueries(), "SELECT VERSION()") {
		t.Fatal("must not fall back on a network error")
	}
}

func TestExternalPoolIsNotClosedOnInitializationError(t *testing.T) {
	pool := newFakePool(t)
	pool.server.replyError("SELECT OB_VERSION()", errors.New("network down"))

	d := &Dialector{Config: &Config{Conn: pool, Mode: ob.ModeMySQL}}
	if _, err := gorm.Open(d, &gorm.Config{DisableAutomaticPing: true}); err == nil {
		t.Fatal("expected initialization error")
	}
	if err := pool.db.Ping(); err != nil {
		t.Fatalf("caller-owned pool must stay usable, ping failed: %v", err)
	}
}

func TestSkipInitializeWithVersionUsesUnknownCapabilities(t *testing.T) {
	pool := newFakePool(t)
	d := &Dialector{Config: &Config{Conn: pool, Mode: ob.ModeMySQL, SkipInitializeWithVersion: true}}
	if _, err := gorm.Open(d, &gorm.Config{DisableAutomaticPing: true}); err != nil {
		t.Fatalf("gorm.Open: %v", err)
	}
	if len(pool.server.recordedQueries()) != 0 {
		t.Fatalf("version must not be queried, got %v", pool.server.recordedQueries())
	}
	if !d.DontSupportForShareClause {
		t.Fatalf("MySQL 5.7 profile must rewrite FOR SHARE: %+v", d.Config)
	}
}

func TestServerVersionSkipsQuery(t *testing.T) {
	pool := newFakePool(t)
	d := &Dialector{Config: &Config{Conn: pool, Mode: ob.ModeMySQL, ServerVersion: testExplicitVersion}}
	if _, err := gorm.Open(d, &gorm.Config{DisableAutomaticPing: true}); err != nil {
		t.Fatalf("gorm.Open: %v", err)
	}
	if len(pool.server.recordedQueries()) != 0 {
		t.Fatalf("explicit ServerVersion must skip the query, got %v", pool.server.recordedQueries())
	}
}

func TestNowFuncInstalledOnlyWhenUserDidNotSetOne(t *testing.T) {
	pool := newFakePool(t)
	pool.server.reply("SELECT OB_VERSION()", testOBCEVersion)
	d := &Dialector{Config: &Config{Conn: pool, Mode: ob.ModeMySQL}}
	db, err := gorm.Open(d, &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatalf("gorm.Open: %v", err)
	}
	if db.NowFunc == nil {
		t.Fatal("NowFunc must be installed")
	}
	if got := db.NowFunc().Nanosecond() % 1e6; got != 0 {
		t.Fatalf("NowFunc must truncate to milliseconds, got nanos=%d", got)
	}

	custom := func() time.Time { return time.Unix(1, 2) }
	db2, err := gorm.Open(&Dialector{Config: &Config{Conn: newFakePool(t), Mode: ob.ModeMySQL, ServerVersion: testExplicitVersion}}, &gorm.Config{NowFunc: custom, DisableAutomaticPing: true})
	if err != nil {
		t.Fatalf("gorm.Open: %v", err)
	}
	if got := db2.NowFunc(); !got.Equal(time.Unix(1, 2)) {
		t.Fatalf("user NowFunc must not be replaced, got %v", got)
	}
}

func TestQuoteToUsesMySQLBackticks(t *testing.T) {
	var builder strings.Builder
	dialectorWithMode(t, ob.ModeMySQL).QuoteTo(&builder, "schema.table")
	if builder.String() != "`schema`.`table`" {
		t.Fatalf("QuoteTo = %q", builder.String())
	}
}

func TestDefaultValueOf(t *testing.T) {
	expr, ok := dialectorWithMode(t, ob.ModeMySQL).DefaultValueOf(nil).(clause.Expr)
	if !ok || expr.SQL != "DEFAULT" {
		t.Fatalf("DefaultValueOf = %#v", expr)
	}
}

func TestClauseBuilderOnConflict(t *testing.T) {
	db := openWithFakeDB(t)

	type user struct {
		ID   uint
		Name string
	}

	sql := db.Session(&gorm.Session{DryRun: true}).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "id"}},
			DoUpdates: clause.AssignmentColumns([]string{"name"}),
		}).
		Create(&user{ID: 1, Name: "a"}).Statement.SQL.String()
	if !strings.Contains(sql, "ON DUPLICATE KEY UPDATE") {
		t.Fatalf("SQL = %q", sql)
	}
	if !strings.Contains(sql, "VALUES(`name`)") {
		t.Fatalf("excluded column must become VALUES(col): %q", sql)
	}
}

func TestClauseBuilderValuesEmpty(t *testing.T) {
	db := openWithFakeDB(t)
	stmt := &gorm.Statement{DB: db}
	stmt.Clauses = map[string]clause.Clause{}
	stmt.Clauses[clauseValues] = clause.Clause{Name: clauseValues, Expression: clause.Values{}}
	stmt.BuildClauses = []string{clauseValues}
	stmt.Build(clauseValues)
	if got := stmt.SQL.String(); got != "VALUES()" {
		t.Fatalf("empty values = %q, want VALUES()", got)
	}
}
