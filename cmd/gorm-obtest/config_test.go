package main

import (
	"strings"
	"testing"
)

func TestConfigurationValidation(t *testing.T) {
	valid := `{"targets":[{"name":"mysql","mode":"mysql","dsn_env":"TEST_DSN"}]}`
	for _, input := range []string{
		`{}`, `{"targets":[]}`, strings.Replace(valid, `"mysql","dsn_env"`, `"auto","dsn_env"`, 1),
		strings.Replace(valid, `"dsn_env"`, `"dsn"`, 1), valid + `{}`,
		strings.Replace(valid, `"targets"`, `"typo"`, 1),
		strings.Replace(valid, `"TEST_DSN"`, `""`, 1),
		valid + strings.Repeat(" ", 1<<20),
	} {
		if _, err := readConfig(strings.NewReader(input)); err == nil {
			t.Fatalf("accepted invalid configuration of %d bytes", len(input))
		}
	}
	cfg, err := readConfig(strings.NewReader(valid))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Targets) != 1 || cfg.Timeout != "2m" {
		t.Fatalf("cfg=%+v", cfg)
	}
	if _, err := cfg.selectTargets("", "oracle"); err == nil {
		t.Fatal("empty selection accepted")
	}
}

func TestReportExitCodes(t *testing.T) {
	for _, tc := range []struct {
		statuses []string
		want     int
	}{
		{[]string{statusPass}, 0}, {[]string{statusPass, statusNA}, 0}, {[]string{statusSkip}, 3},
		{[]string{statusPass, statusSkip}, 3}, {[]string{statusFail}, 1}, {nil, 3},
	} {
		r := report{}
		for _, status := range tc.statuses {
			r.Results = append(r.Results, caseResult{Status: status})
		}
		if got := r.exitCode(); got != tc.want {
			t.Fatalf("statuses=%v exit=%d", tc.statuses, got)
		}
	}
}
