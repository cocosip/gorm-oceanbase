package oceanbase

import (
	"strings"
	"testing"

	ob "github.com/cocosip/go-oceanbase"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func TestForShareRewrittenToLockInShareMode(t *testing.T) {
	db := openWithFakeDB(t)

	type user struct {
		ID   uint
		Name string
	}

	sql := db.Session(&gorm.Session{DryRun: true}).
		Clauses(clause.Locking{Strength: "SHARE"}).
		Find(&[]user{}).Statement.SQL.String()
	if !strings.Contains(sql, "LOCK IN SHARE MODE") {
		t.Fatalf("SQL = %q, want LOCK IN SHARE MODE", sql)
	}
	if strings.Contains(sql, "FOR SHARE") {
		t.Fatalf("SQL = %q must not use MySQL 8 syntax", sql)
	}
}

func TestForShareKeptWhenSupported(t *testing.T) {
	pool := newFakePool(t)
	pool.server.reply("SELECT OB_VERSION()", testOBCEVersion)
	dialector := &Dialector{Config: &Config{
		Conn:                      pool,
		Mode:                      ob.ModeMySQL,
		DontSupportForShareClause: false,
		SkipInitializeWithVersion: true,
	}}
	// The MySQL 5.7-compatible gate is applied even when the caller asks for the
	// modern syntax, because OceanBase's MySQL mode shares the 5.7 profile.
	if _, err := gorm.Open(dialector, &gorm.Config{DisableAutomaticPing: true}); err != nil {
		t.Fatalf("gorm.Open: %v", err)
	}
	if !dialector.DontSupportForShareClause {
		t.Fatal("MySQL 5.7 profile must enable the FOR SHARE rewrite")
	}
}

func TestResolveDriverConfigAppliesTimeAndProfileDefaults(t *testing.T) {
	d := &Dialector{Config: &Config{DSN: testDSNNoMode + "?mode=mysql"}}
	cfg, err := d.resolveDriverConfig()
	if err != nil {
		t.Fatalf("resolveDriverConfig: %v", err)
	}
	if !cfg.Session.ParseTime {
		t.Fatal("internal connections must enable ParseTime")
	}
	if cfg.Session.CompatibleMysqlVersion != 8 {
		t.Fatalf("profile = %d, want 8", cfg.Session.CompatibleMysqlVersion)
	}
	if cfg.Mode != ob.ModeMySQL {
		t.Fatalf("mode = %v", cfg.Mode)
	}
}

func TestResolveDriverConfigKeepsExplicitProfileFive(t *testing.T) {
	d := &Dialector{Config: &Config{DSN: testDSNNoMode + "?mode=mysql&compatibleMysqlVersion=5"}}
	cfg, err := d.resolveDriverConfig()
	if err != nil {
		t.Fatalf("resolveDriverConfig: %v", err)
	}
	if cfg.Session.CompatibleMysqlVersion != 5 {
		t.Fatalf("profile = %d, want 5", cfg.Session.CompatibleMysqlVersion)
	}
}

func TestResolveDriverConfigTopProfileWins(t *testing.T) {
	d := &Dialector{Config: &Config{
		DSN:                    testDSNNoMode + "?mode=mysql&compatibleMysqlVersion=5",
		CompatibleMysqlVersion: 8,
	}}
	cfg, err := d.resolveDriverConfig()
	if err != nil {
		t.Fatalf("resolveDriverConfig: %v", err)
	}
	if cfg.Session.CompatibleMysqlVersion != 8 {
		t.Fatalf("profile = %d, want the top-level 8", cfg.Session.CompatibleMysqlVersion)
	}
}

func TestResolveDriverConfigDoesNotTouchExternalConn(t *testing.T) {
	pool := newFakePool(t)
	d := &Dialector{Config: &Config{Conn: pool, Mode: ob.ModeMySQL}}
	cfg, err := d.resolveDriverConfig()
	if err != nil {
		t.Fatalf("resolveDriverConfig: %v", err)
	}
	if cfg.Session.ParseTime {
		t.Fatal("external connections are configured by the caller")
	}
}
