package oceanbase

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
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

func TestOracleQuoteTo(t *testing.T) {
	d := dialectorWithMode(t, ob.ModeOracle)
	quote := func(str string) string {
		var sb strings.Builder
		d.QuoteTo(&sb, str)
		return sb.String()
	}

	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "plain identifier", in: "users", want: `"USERS"`},
		{name: "snake case", in: "created_at", want: `"CREATED_AT"`},
		{name: "qualified", in: "users.id", want: `"USERS"."ID"`},
		{name: "schema qualified", in: "db.users.id", want: `"DB"."USERS"."ID"`},
		{name: "explicit case preserved", in: `"Foo"`, want: `"Foo"`},
		{name: "explicit part keeps case, rest upper", in: `"Foo".bar`, want: `"Foo"."BAR"`},
		{name: "embedded quote escaped", in: `a"b`, want: `"A""B"`},
		{name: "empty identifier", in: "", want: `"`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := quote(tc.in); got != tc.want {
				t.Fatalf("QuoteTo(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestOracleDryRunQuoting(t *testing.T) {
	db, _, _ := openOracleWithFakeDB(t)
	dry := db.Session(&gorm.Session{DryRun: true})

	type oracleUser struct {
		ID   uint   `gorm:"primaryKey"`
		Name string `gorm:"size:64"`
	}

	createSQL := dry.Create(&oracleUser{Name: testUserName}).Statement.SQL.String()
	if createSQL != `INSERT INTO "ORACLE_USERS" ("NAME") VALUES (?)` {
		t.Fatalf("create SQL = %q", createSQL)
	}

	querySQL := dry.Model(&oracleUser{}).Where(`"NAME" = ?`, testUserName).Find(&[]oracleUser{}).Statement.SQL.String()
	wantQuery := `SELECT * FROM "ORACLE_USERS" WHERE "NAME" = ?`
	if querySQL != wantQuery {
		t.Fatalf("query SQL = %q, want %q", querySQL, wantQuery)
	}
}

func TestOracleScanMapsUppercaseColumns(t *testing.T) {
	pool := newFakePool(t)
	pool.server.reply("SELECT ob_version() FROM dual", testOBCEVersion)

	type oracleRow struct {
		Name string `gorm:"size:64"`
	}

	db, err := gorm.Open(&Dialector{Config: &Config{Conn: pool, Mode: ob.ModeOracle}}, &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatalf("gorm.Open: %v", err)
	}

	selectSQL := db.Session(&gorm.Session{DryRun: true}).
		Model(&oracleRow{}).Where(`"NAME" = ?`, testUserName).Find(&[]oracleRow{}).Statement.SQL.String()
	pool.server.replyRows(selectSQL, []string{"NAME"}, [][]driver.Value{{testUserName}})

	var rows []oracleRow
	if err := db.Model(&oracleRow{}).Where(`"NAME" = ?`, testUserName).Find(&rows).Error; err != nil {
		t.Fatalf("Find: %v", err)
	}
	if len(rows) != 1 || rows[0].Name != testUserName {
		t.Fatalf("scan result = %+v, want one row named alice", rows)
	}
}
