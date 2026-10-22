package oceanbase

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	ob "github.com/cocosip/go-oceanbase"
	"gorm.io/gorm"
)

// openOracleWithFakeDB boots a MySQL-free Oracle-mode gorm.DB against the fake
// server, answering the Oracle version query.
func openOracleWithFakeDB(t *testing.T) (*gorm.DB, *Dialector, *fakePool) {
	t.Helper()
	pool := newFakePool(t)
	pool.server.reply("SELECT ob_version() FROM dual", testOBCEVersion)
	dialector := &Dialector{Config: &Config{Conn: pool, Mode: ob.ModeOracle}}
	db, err := gorm.Open(dialector, &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatalf("gorm.Open: %v", err)
	}
	return db, dialector, pool
}

// stubSessionReader replaces the auto-probe session variable reader and
// restores the original when the test ends.
func stubSessionReader(t *testing.T, fn func(ctx context.Context, cfg ob.Config) (sql.NullString, error)) {
	t.Helper()
	orig := sessionVariableReader
	sessionVariableReader = fn
	t.Cleanup(func() { sessionVariableReader = orig })
}

func TestOracleModeOpensAndProbesVersionFromDual(t *testing.T) {
	db, dialector, pool := openOracleWithFakeDB(t)
	if dialector.resolvedMode != ob.ModeOracle {
		t.Fatalf("resolvedMode = %v, want ModeOracle", dialector.resolvedMode)
	}
	if dialector.ServerVersion != testOBCEVersion {
		t.Fatalf("ServerVersion = %q", dialector.ServerVersion)
	}
	if !containsQuery(pool.server.recordedQueries(), "SELECT ob_version() FROM dual") {
		t.Fatalf("Oracle mode must query ob_version() FROM dual, got %v", pool.server.recordedQueries())
	}
	if db.NowFunc == nil {
		t.Fatal("NowFunc must be installed")
	}
}

func TestOracleNowFuncTruncatesToMicroseconds(t *testing.T) {
	db, _, _ := openOracleWithFakeDB(t)
	if got := db.NowFunc().Nanosecond() % 1e3; got != 0 {
		t.Fatalf("Oracle NowFunc must truncate to microseconds, got nanos=%d", got)
	}

	custom := func() time.Time { return time.Unix(1, 2) }
	pool := newFakePool(t)
	db2, err := gorm.Open(&Dialector{Config: &Config{
		Conn: pool, Mode: ob.ModeOracle, ServerVersion: testExplicitVersion,
	}}, &gorm.Config{NowFunc: custom, DisableAutomaticPing: true})
	if err != nil {
		t.Fatalf("gorm.Open: %v", err)
	}
	if db2.NowFunc().Nanosecond() != 2 {
		t.Fatal("user NowFunc must not be replaced")
	}
}

func TestOracleModeSkipsMySQLSyntaxGates(t *testing.T) {
	_, dialector, _ := openOracleWithFakeDB(t)
	if dialector.DontSupportForShareClause || dialector.DontSupportRenameColumn || dialector.DontSupportDropConstraint {
		t.Fatalf("MySQL 5.7 gates must not apply to Oracle mode: %+v", dialector.Config)
	}
}

func TestOracleModeDoesNotInjectMySQLProfile(t *testing.T) {
	// A DSN carries the driver's own default profile after parsing; the
	// dialect only has to keep its 8 to itself.
	d := &Dialector{Config: &Config{DSN: testDSNNoMode + "?mode=oracle"}}
	cfg, err := d.resolveDriverConfig()
	if err != nil {
		t.Fatalf("resolveDriverConfig: %v", err)
	}
	if cfg.Mode != ob.ModeOracle {
		t.Fatalf("mode = %v, want ModeOracle", cfg.Mode)
	}
	if !cfg.Session.ParseTime {
		t.Fatal("internal connections must enable ParseTime")
	}
	if cfg.Session.CompatibleMysqlVersion == 8 {
		t.Fatal("MySQL profile 8 must not be injected for Oracle mode")
	}

	// A structured config without a profile stays profile-free.
	original := ob.Config{Addr: testAddr, User: "u", Mode: ob.ModeOracle}
	d2 := &Dialector{Config: &Config{DSNConfig: &original}}
	cfg2, err := d2.resolveDriverConfig()
	if err != nil {
		t.Fatalf("resolveDriverConfig: %v", err)
	}
	if cfg2.Session.CompatibleMysqlVersion != 0 {
		t.Fatalf("MySQL profile must not be injected for Oracle mode, got %d", cfg2.Session.CompatibleMysqlVersion)
	}
	if original.Session.ParseTime {
		t.Fatal("caller DSNConfig must not be modified")
	}
}

func TestAutoProbeResolvesOracle(t *testing.T) {
	var received ob.Config
	stubSessionReader(t, func(_ context.Context, cfg ob.Config) (sql.NullString, error) {
		received = cfg
		return sql.NullString{String: "ORACLE", Valid: true}, nil
	})

	d := &Dialector{Config: &Config{DSN: testDSNNoMode + "?mode=auto"}}
	cfg, err := d.resolveDriverConfig()
	if err != nil {
		t.Fatalf("resolveDriverConfig: %v", err)
	}
	if cfg.Mode != ob.ModeOracle || d.resolvedMode != ob.ModeOracle {
		t.Fatalf("mode = %v, want ModeOracle", cfg.Mode)
	}
	if received.Mode != ob.ModeAuto {
		t.Fatalf("probe must run with ModeAuto, got %v", received.Mode)
	}
}

func TestAutoProbeErrorPropagates(t *testing.T) {
	probeErr := errors.New("permission denied")
	stubSessionReader(t, func(context.Context, ob.Config) (sql.NullString, error) {
		return sql.NullString{}, probeErr
	})

	d := &Dialector{Config: &Config{DSN: testDSNNoMode + "?mode=auto"}}
	_, err := d.resolveDriverConfig()
	if !errors.Is(err, ErrInvalidConfiguration) || !errors.Is(err, probeErr) {
		t.Fatalf("err = %v, want ErrInvalidConfiguration wrapping the probe error", err)
	}
}
