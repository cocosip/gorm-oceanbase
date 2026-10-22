package oceanbase

import (
	"database/sql"
	"errors"
	"testing"

	ob "github.com/cocosip/go-oceanbase"
)

func TestParseCompatibilityMode(t *testing.T) {
	cases := []struct {
		name  string
		value sql.NullString
		want  ob.Mode
		fails bool
	}{
		{name: "mysql", value: sql.NullString{String: "MYSQL", Valid: true}, want: ob.ModeMySQL},
		{name: "oracle lower", value: sql.NullString{String: "oracle", Valid: true}, want: ob.ModeOracle},
		{name: "padded", value: sql.NullString{String: " MySQL ", Valid: true}, want: ob.ModeMySQL},
		{name: "null", value: sql.NullString{}, fails: true},
		{name: "empty", value: sql.NullString{String: "", Valid: true}, fails: true},
		{name: "unknown", value: sql.NullString{String: "PG", Valid: true}, fails: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseCompatibilityMode(tc.value)
			if tc.fails {
				if !errors.Is(err, ErrInvalidConfiguration) {
					t.Fatalf("err = %v, want ErrInvalidConfiguration", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if got != tc.want {
				t.Fatalf("mode = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestProbeConfigForDSNUsesAutoMode(t *testing.T) {
	d := &Dialector{Config: &Config{DSN: testDSNModeMySQL}}
	cfg, err := d.probeConfig()
	if err != nil {
		t.Fatalf("probeConfig: %v", err)
	}
	if cfg.Mode != ob.ModeAuto {
		t.Fatalf("probe mode = %v, want ModeAuto", cfg.Mode)
	}
	if !cfg.Session.ParseTime {
		t.Fatal("probe must enable ParseTime")
	}
	if cfg.User != "u" || cfg.Database != "db" {
		t.Fatalf("probe config lost DSN fields: user=%q db=%q", cfg.User, cfg.Database)
	}
}

func TestProbeConfigDoesNotMutateCallerDSNConfig(t *testing.T) {
	original := ob.Config{Addr: "127.0.0.1:2881", User: "u", Mode: ob.ModeAuto}
	d := &Dialector{Config: &Config{DSNConfig: &original}}
	if _, err := d.probeConfig(); err != nil {
		t.Fatalf("probeConfig: %v", err)
	}
	if original.Session.ParseTime {
		t.Fatal("probeConfig must copy the caller config")
	}
}

func TestResolveModePrecedence(t *testing.T) {
	cases := []struct {
		name        string
		topMode     ob.Mode
		dsnMode     ob.Mode
		dsnExplicit bool
		viaConfig   bool
		want        ob.Mode
	}{
		{name: "default mysql", want: ob.ModeMySQL},
		{name: "top overrides dsn", topMode: ob.ModeOracle, dsnMode: ob.ModeMySQL, dsnExplicit: true, want: ob.ModeOracle},
		{name: "dsn explicit wins over default", dsnMode: ob.ModeOracle, dsnExplicit: true, want: ob.ModeOracle},
		{name: "dsnconfig non-zero", dsnMode: ob.ModeOracle, viaConfig: true, want: ob.ModeOracle},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := &Dialector{Config: &Config{Mode: tc.topMode}}
			if tc.viaConfig {
				d.DSNConfig = &ob.Config{Mode: tc.dsnMode}
			}
			got, err := d.resolveMode(ob.Config{Mode: tc.dsnMode}, tc.dsnExplicit)
			if err != nil {
				t.Fatalf("resolveMode: %v", err)
			}
			if got != tc.want {
				t.Fatalf("mode = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestResolveModeRejectsUnknownMode(t *testing.T) {
	d := &Dialector{Config: &Config{Mode: ob.Mode(99)}}
	if _, err := d.resolveMode(ob.Config{}, false); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("err = %v, want ErrInvalidConfiguration", err)
	}
}
