package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"time"
)

type configuration struct {
	Targets        []targetConfig `json:"targets"`
	Cases          []string       `json:"cases,omitempty"`
	Timeout        string         `json:"timeout,omitempty"`
	CleanupTimeout string         `json:"cleanup_timeout,omitempty"`
}

type targetConfig struct {
	Name         string `json:"name"`
	Mode         string `json:"mode"`
	DSNEnv       string `json:"dsn_env"`
	PrepareStmt  bool   `json:"prepare_stmt,omitempty"`
	MySQLProfile int    `json:"mysql_profile,omitempty"`
}

var identifier = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,63}$`)

func readConfig(r io.Reader) (configuration, error) {
	var cfg configuration
	data, err := io.ReadAll(io.LimitReader(r, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return cfg, errors.New("configuration cannot be read or exceeds 1 MiB")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&cfg); err != nil {
		return cfg, errors.New("invalid configuration JSON or unknown field")
	}
	if err := d.Decode(new(any)); !errors.Is(err, io.EOF) {
		return cfg, errors.New("configuration requires exactly one JSON object")
	}
	if len(cfg.Targets) == 0 {
		return cfg, errors.New("at least one target is required")
	}
	seen := map[string]bool{}
	for i, target := range cfg.Targets {
		if !identifier.MatchString(target.Name) || seen[target.Name] {
			return cfg, errors.New("target names must be unique ASCII identifiers")
		}
		seen[target.Name] = true
		if target.Mode != modeMySQL && target.Mode != modeOracle {
			return cfg, errors.New("target mode must be mysql or oracle")
		}
		if !identifier.MatchString(target.DSNEnv) {
			return cfg, errors.New("dsn_env must name an environment variable")
		}
		if target.MySQLProfile != 0 && (target.Mode != modeMySQL || target.MySQLProfile != 5 && target.MySQLProfile != 8) {
			return cfg, errors.New("mysql_profile must be 5 or 8 for a MySQL target")
		}
		if target.Mode == modeMySQL && target.MySQLProfile == 0 {
			cfg.Targets[i].MySQLProfile = 8
		}
	}
	if cfg.Timeout == "" {
		cfg.Timeout = "2m"
	}
	if cfg.CleanupTimeout == "" {
		cfg.CleanupTimeout = "30s"
	}
	for _, value := range []string{cfg.Timeout, cfg.CleanupTimeout} {
		duration, err := time.ParseDuration(value)
		if err != nil || duration <= 0 || duration > time.Hour {
			return cfg, errors.New("timeouts must be positive durations no greater than 1h")
		}
	}
	known := map[string]bool{}
	for _, c := range caseDefinitions() {
		known[c.name] = true
	}
	seen = map[string]bool{}
	for _, name := range cfg.Cases {
		if !known[name] || seen[name] {
			return cfg, errors.New("unknown or duplicate case")
		}
		seen[name] = true
	}
	return cfg, nil
}

func (c configuration) selectTargets(name, mode string) (configuration, error) {
	if mode != "all" && mode != modeMySQL && mode != modeOracle && mode != "" {
		return c, errors.New("mode must be all, mysql or oracle")
	}
	selected := make([]targetConfig, 0, len(c.Targets))
	for _, target := range c.Targets {
		if (name == "" || target.Name == name) && (mode == "" || mode == "all" || target.Mode == mode) {
			selected = append(selected, target)
		}
	}
	if len(selected) == 0 {
		return c, errors.New("no targets match selection")
	}
	c.Targets = selected
	return c, nil
}
