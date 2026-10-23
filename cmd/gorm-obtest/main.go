// Command gorm-obtest runs GORM acceptance cases against configured OceanBase tenants.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
)

func main() { os.Exit(command(os.Args[1:], os.Stdout, os.Stderr)) }

func command(args []string, out, stderr io.Writer) int {
	f := flag.NewFlagSet("gorm-obtest", flag.ContinueOnError)
	f.SetOutput(stderr)
	path := f.String("config", "", "JSON configuration file")
	mode := f.String("mode", "all", "all, mysql or oracle")
	target := f.String("target", "", "select one configured target")
	list := f.Bool("list", false, "list cases without connecting")
	check := f.Bool("check", false, "validate configuration without reading credentials or connecting")
	reportPath := f.String("report", "gorm-obtest-report.json", "JSON report destination")
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if f.NArg() != 0 {
		_, _ = fmt.Fprintln(stderr, "unexpected positional arguments")
		return 2
	}
	if *list {
		for _, c := range caseDefinitions() {
			_, _ = fmt.Fprintf(out, "%s\t%s\t%s\n", c.name, c.mode, c.description)
		}
		return 0
	}
	file, err := os.Open(*path)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "cannot open configuration file; use -config")
		return 2
	}
	cfg, err := readConfig(file)
	_ = file.Close()
	if err == nil {
		cfg, err = cfg.selectTargets(*target, *mode)
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 2
	}
	if *check {
		_, _ = fmt.Fprintf(out, "Configuration valid: %d target(s); credentials not read; no connection made\n", len(cfg.Targets))
		return 0
	}
	// Validate all credentials before any target creates objects. Never print DSNs.
	connections, err := connectionConfigs(cfg)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 2
	}
	reportFile, err := os.OpenFile(*reportPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "cannot create report file (must not already exist)")
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	r := runSuite(ctx, cfg, connections, out)
	encoder := json.NewEncoder(reportFile)
	encoder.SetIndent("", "  ")
	writeErr := encoder.Encode(r)
	closeErr := reportFile.Close()
	if writeErr != nil || closeErr != nil {
		_, _ = fmt.Fprintln(stderr, "cannot save report")
		return 1
	}
	_, _ = fmt.Fprintf(out, "pass=%d fail=%d skip=%d not_applicable=%d\n", r.count(statusPass), r.count(statusFail), r.count(statusSkip), r.count(statusNA))
	return r.exitCode()
}
