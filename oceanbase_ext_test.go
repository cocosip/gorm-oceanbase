package oceanbase

import (
	"errors"
	"strings"
	"testing"
	"time"

	ob "github.com/cocosip/go-oceanbase"
	"gorm.io/gorm"
)

func TestNewCopiesCallerConfig(t *testing.T) {
	cfg := ob.Config{
		Addr:     "127.0.0.1:2881",
		User:     "u",
		Password: "p",
		Database: "db",
		Mode:     ob.ModeMySQL,
		Session:  ob.SessionOptions{CompatibleMysqlVersion: 5},
	}
	dialector := New(Config{DSNConfig: &cfg}).(*Dialector)
	driverCfg, err := dialector.resolveDriverConfig()
	if err != nil {
		t.Fatalf("resolveDriverConfig: %v", err)
	}
	if cfg.Session.ParseTime {
		t.Fatal("caller DSNConfig must not be modified")
	}
	if cfg.Session.CompatibleMysqlVersion != 5 {
		t.Fatalf("caller profile changed: %d", cfg.Session.CompatibleMysqlVersion)
	}
	if !driverCfg.Session.ParseTime {
		t.Fatal("internal config must enable ParseTime")
	}
	if driverCfg.Session.CompatibleMysqlVersion != 5 {
		t.Fatalf("normalized DSNConfig profile 5 must be kept, got %d", driverCfg.Session.CompatibleMysqlVersion)
	}
}

func TestResolveMySQLProfilePrecedence(t *testing.T) {
	cases := []struct {
		name        string
		top         int
		dsnCfg      int
		dsnExplicit bool
		fromConfig  *ob.Config
		want        int
	}{
		{name: "default eight", want: 8},
		{name: "top wins", top: 5, dsnCfg: 8, want: 5},
		{name: "dsn explicit five", dsnCfg: 5, dsnExplicit: true, want: 5},
		{name: "dsnconfig normalized five", fromConfig: &ob.Config{Session: ob.SessionOptions{CompatibleMysqlVersion: 5}}, want: 5},
		{name: "dsnconfig zero defaults eight", fromConfig: &ob.Config{}, want: 8},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := &Dialector{Config: &Config{
				CompatibleMysqlVersion: tc.top,
				DSNConfig:              tc.fromConfig,
			}}
			got := d.resolveMySQLProfile(ob.Config{Session: ob.SessionOptions{CompatibleMysqlVersion: tc.dsnCfg}}, tc.dsnExplicit)
			if got != tc.want {
				t.Fatalf("profile = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestDSNParseTimeFalseIsRejected(t *testing.T) {
	d := &Dialector{Config: &Config{DSN: testDSNNoMode + "?mode=mysql&parseTime=false"}}
	_, err := d.resolveDriverConfig()
	if !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("err = %v, want ErrInvalidConfiguration", err)
	}
	if !strings.Contains(err.Error(), "parseTime=false") {
		t.Fatalf("error must explain the parseTime conflict: %v", err)
	}
}

func TestDSNModeOracleRejectedBeforeConnecting(t *testing.T) {
	d := &Dialector{Config: &Config{DSN: testDSNNoMode + "?mode=oracle"}}
	_, err := d.resolveDriverConfig()
	if !errors.Is(err, ErrUnsupportedMode) {
		t.Fatalf("err = %v, want ErrUnsupportedMode", err)
	}
}

func TestDSNInvalidReturnsInitializeError(t *testing.T) {
	if _, err := gorm.Open(Open("not-a-dsn"), &gorm.Config{DisableAutomaticPing: true}); err == nil {
		t.Fatal("expected initialization error")
	}
}

func TestOpenDoesNotConnect(t *testing.T) {
	d := Open(testDSNModeMySQL).(*Dialector)
	if d.DSN != testDSNModeMySQL {
		t.Fatalf("DSN = %q", d.DSN)
	}
}

func TestApplyRemembersUserNowFunc(t *testing.T) {
	d := &Dialector{Config: &Config{}}
	if err := d.Apply(&gorm.Config{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !d.defaultNowFunc {
		t.Fatal("default NowFunc must be recorded as installable")
	}
	if err := d.Apply(&gorm.Config{NowFunc: time.Now}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if d.defaultNowFunc {
		t.Fatal("user NowFunc must be left untouched")
	}
}

func TestExplainUsesQuestionPlaceholders(t *testing.T) {
	d := dialectorWithMode(t, ob.ModeMySQL)
	got := d.Explain("SELECT * FROM `users` WHERE `id` = ?", 7)
	if got != "SELECT * FROM `users` WHERE `id` = 7" {
		t.Fatalf("Explain = %q", got)
	}
}
