package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"time"

	ob "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/dberrors"
	oceanbase "github.com/cocosip/gorm-oceanbase"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

const (
	statusPass  = "pass"
	statusFail  = "fail"
	statusSkip  = "skip"
	statusNA    = "not_applicable"
	modeBoth    = "both"
	modeMySQL   = "mysql"
	modeOracle  = "oracle"
	objectTable = "table"
)

type caseDefinition struct {
	name, mode, description string
	run                     func(*scenario) error
}
type report struct {
	RunID     string       `json:"run_id"`
	Started   time.Time    `json:"started"`
	Duration  string       `json:"duration"`
	GoVersion string       `json:"go_version"`
	Platform  string       `json:"platform"`
	Results   []caseResult `json:"results"`
}
type caseResult struct {
	Target        string       `json:"target"`
	Mode          string       `json:"mode"`
	Case          string       `json:"case"`
	Status        string       `json:"status"`
	Duration      string       `json:"duration"`
	Message       string       `json:"message,omitempty"`
	Stage         string       `json:"last_stage,omitempty"`
	Version       string       `json:"server_version,omitempty"`
	PrepareStmt   bool         `json:"prepare_stmt"`
	MySQLProfile  int          `json:"mysql_profile,omitempty"`
	Columns       []columnInfo `json:"columns,omitempty"`
	Indexes       []indexInfo  `json:"indexes,omitempty"`
	Objects       []string     `json:"objects,omitempty"`
	CleanupErrors []string     `json:"cleanup_errors,omitempty"`
}
type columnInfo struct {
	Name          string  `json:"name"`
	Type          string  `json:"database_type"`
	Length        *int64  `json:"length,omitempty"`
	Precision     *int64  `json:"precision,omitempty"`
	Scale         *int64  `json:"scale,omitempty"`
	Nullable      *bool   `json:"nullable,omitempty"`
	PrimaryKey    *bool   `json:"primary_key,omitempty"`
	AutoIncrement *bool   `json:"auto_increment,omitempty"`
	Default       *string `json:"default,omitempty"`
	Comment       *string `json:"comment,omitempty"`
}
type indexInfo struct {
	Name    string   `json:"name"`
	Columns []string `json:"columns"`
	Unique  *bool    `json:"unique,omitempty"`
}
type cleanupAction struct {
	name string
	run  func(*gorm.DB) error
}
type scenario struct {
	db             *gorm.DB
	base           *gorm.DB
	mode, prefix   string
	stage          string
	mysqlProfile   int
	seq            int
	cleanupTimeout time.Duration
	cleanups       []cleanupAction
	columns        []columnInfo
	indexes        []indexInfo
	objects        []string
}
type checkFailure string

func (e checkFailure) Error() string { return string(e) }
func expect(ok bool, message string) error {
	if !ok {
		return checkFailure(message)
	}
	return nil
}

// Only controlled assertions and error codes reach reports. Driver messages can contain credentials.
func errorSummary(err error) string {
	var check checkFailure
	if errors.As(err, &check) {
		return string(check)
	}
	if errors.Is(err, context.Canceled) {
		return "operation canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "operation timed out"
	}
	var server *dberrors.Error
	if errors.As(err, &server) {
		return fmt.Sprintf("server error code=%d", server.Code)
	}
	return fmt.Sprintf("operation failed (%T; raw message omitted)", err)
}
func (r report) count(status string) int {
	n := 0
	for _, result := range r.Results {
		if result.Status == status {
			n++
		}
	}
	return n
}
func (r report) exitCode() int {
	if r.count(statusFail) > 0 {
		return 1
	}
	if r.count(statusSkip) > 0 || r.count(statusPass) == 0 {
		return 3
	}
	return 0
}

func connectionConfigs(cfg configuration) ([]oceanbase.Config, error) {
	connections := make([]oceanbase.Config, 0, len(cfg.Targets))
	for _, target := range cfg.Targets {
		dsn := os.Getenv(target.DSNEnv)
		if dsn == "" {
			return nil, fmt.Errorf("%s: configured DSN environment variable is empty", target.Name)
		}
		parsed, err := ob.ParseDSN(dsn)
		if err != nil || parsed.CreateDatabaseIfMissing {
			return nil, fmt.Errorf("%s: invalid DSN or createDatabaseIfMissing enabled", target.Name)
		}
		mode := ob.ModeMySQL
		if target.Mode == modeOracle {
			mode = ob.ModeOracle
		}
		if parsed.Mode != 0 && parsed.Mode != mode {
			return nil, fmt.Errorf("%s: DSN mode conflicts with target mode", target.Name)
		}
		parsed.Session.Location = time.UTC
		timeout, _ := time.ParseDuration(cfg.Timeout)
		if timeout > 0 {
			if parsed.ConnectTimeout == 0 || parsed.ConnectTimeout > timeout {
				parsed.ConnectTimeout = timeout
			}
			if parsed.ReadTimeout == 0 || parsed.ReadTimeout > timeout {
				parsed.ReadTimeout = timeout
			}
		}
		connections = append(connections, oceanbase.Config{DSNConfig: &parsed, Mode: mode, CompatibleMysqlVersion: target.MySQLProfile})
	}
	return connections, nil
}

func runSuite(ctx context.Context, cfg configuration, connections []oceanbase.Config, out io.Writer) report {
	started := time.Now()
	nonce := make([]byte, 6)
	r := report{Started: started.UTC(), GoVersion: runtime.Version(), Platform: runtime.GOOS + "/" + runtime.GOARCH}
	if _, err := rand.Read(nonce); err != nil {
		r.Results = append(r.Results, caseResult{Case: "startup", Status: statusFail, Message: "cannot generate run identity"})
		return r
	}
	r.RunID = hex.EncodeToString(nonce)
	timeout, _ := time.ParseDuration(cfg.Timeout)
	cleanupTimeout, _ := time.ParseDuration(cfg.CleanupTimeout)
	selected := map[string]bool{}
	for _, name := range cfg.Cases {
		selected[name] = true
	}
	for i, target := range cfg.Targets {
		openCtx, cancel := context.WithTimeout(ctx, timeout)
		db, err := gorm.Open(oceanbase.New(connections[i]), &gorm.Config{DefaultContextTimeout: timeout, Logger: logger.Default.LogMode(logger.Silent), TranslateError: true, PrepareStmt: target.PrepareStmt})
		if err == nil {
			var poolErr error
			pool, poolErr := db.DB()
			err = poolErr
			if err == nil {
				err = pool.PingContext(openCtx)
				if err == nil {
					err = verifyMode(openCtx, db, target.Mode)
				}
			}
		}
		cancel()
		if err != nil {
			r.Results = append(r.Results, caseResult{Target: target.Name, Mode: target.Mode, Case: "connect", Status: statusFail, Message: errorSummary(err), PrepareStmt: target.PrepareStmt, MySQLProfile: target.MySQLProfile})
			for _, c := range caseDefinitions() {
				if len(selected) > 0 && !selected[c.name] {
					continue
				}
				status, message := statusSkip, "connection failed; case not run"
				if c.mode != modeBoth && c.mode != target.Mode {
					status, message = statusNA, "case applies to "+c.mode+" mode"
				}
				r.Results = append(r.Results, caseResult{Target: target.Name, Mode: target.Mode, Case: c.name, Status: status, Message: message, PrepareStmt: target.PrepareStmt, MySQLProfile: target.MySQLProfile})
			}
			if db != nil {
				closeDB(db)
			}
			continue
		}
		version := db.Dialector.(*oceanbase.Dialector).ServerVersion
		for j, c := range caseDefinitions() {
			if len(selected) > 0 && !selected[c.name] {
				continue
			}
			result := caseResult{Target: target.Name, Mode: target.Mode, Case: c.name, Version: version, PrepareStmt: target.PrepareStmt, Status: statusNA, Message: "case applies to " + c.mode + " mode"}
			if c.mode == modeBoth || c.mode == target.Mode {
				caseCtx, caseCancel := context.WithTimeout(ctx, timeout)
				s := &scenario{base: db, db: db.WithContext(caseCtx), mode: target.Mode, mysqlProfile: target.MySQLProfile, prefix: fmt.Sprintf("ga%s%02x%03x", r.RunID, i, j), cleanupTimeout: cleanupTimeout}
				result = runScenario(s, c.run)
				caseCancel()
				result.Target, result.Mode, result.Case, result.Version, result.PrepareStmt = target.Name, target.Mode, c.name, version, target.PrepareStmt
			}
			result.MySQLProfile = target.MySQLProfile
			r.Results = append(r.Results, result)
			_, _ = fmt.Fprintf(out, "%s %s %s\n", target.Name, c.name, result.Status)
		}
		closeDB(db)
	}
	r.Duration = time.Since(started).String()
	return r
}

func verifyMode(ctx context.Context, db *gorm.DB, want string) error {
	pool, err := db.DB()
	if err != nil {
		return err
	}
	conn, err := pool.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	value, err := ob.SessionVariable(ctx, conn, "ob_compatibility_mode")
	if err != nil {
		return err
	}
	return expect(value.Valid && strings.EqualFold(value.String, want), "connected tenant compatibility mode differs from configured target")
}

func closeDB(db *gorm.DB) {
	if pool, err := db.DB(); err == nil {
		_ = pool.Close()
	}
}

func runScenario(s *scenario, run func(*scenario) error) (result caseResult) {
	start := time.Now()
	result.Status = statusPass
	defer func() {
		if recover() != nil {
			result.Status = statusFail
			result.Message = "case panicked (raw panic omitted)"
		}
		for i := len(s.cleanups) - 1; i >= 0; i-- {
			action := s.cleanups[i]
			ctx, cancel := context.WithTimeout(context.Background(), s.cleanupTimeout)
			err := runCleanup(action, s.base.WithContext(ctx))
			cancel()
			if err != nil {
				result.Status = statusFail
				result.CleanupErrors = append(result.CleanupErrors, action.name+": "+errorSummary(err))
			}
		}
		result.Columns, result.Indexes, result.Objects = s.columns, s.indexes, s.objects
		result.Stage = s.stage
		result.Duration = time.Since(start).String()
	}()
	if err := run(s); err != nil {
		result.Status = statusFail
		result.Message = errorSummary(err)
	}
	return result
}

func runCleanup(action cleanupAction, db *gorm.DB) (err error) {
	defer func() {
		if recover() != nil {
			err = checkFailure("cleanup panicked (raw panic omitted)")
		}
	}()
	return action.run(db)
}

func (s *scenario) name() string { s.seq++; return fmt.Sprintf("%s_%d", s.prefix, s.seq) }
func (s *scenario) quote(name string) string {
	stmt := &gorm.Statement{DB: s.db}
	stmt.WriteQuoted(clause.Column{Name: name})
	return stmt.SQL.String()
}

func (s *scenario) createTable(model any) (string, error) {
	name := s.name()
	return name, s.createNamedTable(s.db, name, model)
}

func (s *scenario) createNamedTable(base *gorm.DB, name string, model any) error {
	// Fixtures have no index tags: indexes are tested after registering table ownership.
	db := base.Session(&gorm.Session{NewDB: true})
	db.DisableForeignKeyConstraintWhenMigrating = true
	db.IgnoreRelationshipsWhenMigrating = true
	err := db.Table(name).Migrator().CreateTable(model)
	if err != nil {
		return err
	}
	s.objects = append(s.objects, name)
	s.cleanups = append(s.cleanups, cleanupAction{name: name, run: func(db *gorm.DB) error {
		if err := db.Exec("DROP TABLE ?", clause.Table{Name: name}).Error; err != nil {
			return err
		}
		check := &scenario{db: db, mode: s.mode}
		return check.assertAbsent(objectTable, name, name)
	}})
	return nil
}

func (s *scenario) assertAbsent(kind, table, name string) error {
	mysql := map[string]string{
		objectTable:  "information_schema.tables WHERE table_schema = DATABASE() AND table_name = ?",
		"column":     "information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? AND column_name = ?",
		"index":      "information_schema.statistics WHERE table_schema = DATABASE() AND table_name = ? AND index_name = ?",
		"constraint": "information_schema.table_constraints WHERE constraint_schema = DATABASE() AND table_name = ? AND constraint_name = ?",
	}
	oracle := map[string]string{
		objectTable:  "all_tables WHERE owner = SYS_CONTEXT('USERENV','CURRENT_SCHEMA') AND table_name = ?",
		"column":     "all_tab_columns WHERE owner = SYS_CONTEXT('USERENV','CURRENT_SCHEMA') AND table_name = ? AND column_name = ?",
		"index":      "all_indexes WHERE table_owner = SYS_CONTEXT('USERENV','CURRENT_SCHEMA') AND table_name = ? AND index_name = ?",
		"constraint": "all_constraints WHERE owner = SYS_CONTEXT('USERENV','CURRENT_SCHEMA') AND table_name = ? AND constraint_name = ?",
	}
	query := mysql[kind]
	if s.mode == modeOracle {
		query, table, name = oracle[kind], canonical(table), canonical(name)
	}
	if query == "" {
		return checkFailure("invalid metadata object kind")
	}
	args := []any{table}
	if kind != objectTable {
		args = append(args, name)
	}
	var count int64
	if err := s.db.Raw("SELECT COUNT(*) FROM "+query, args...).Scan(&count).Error; err != nil {
		return err
	}
	return expect(count == 0, "removed object still exists in metadata")
}

func (s *scenario) metadata(table string, model any) error {
	columns, err := s.db.Table(table).Migrator().ColumnTypes(model)
	if err != nil {
		return err
	}
	for _, column := range columns {
		info := columnInfo{Name: column.Name(), Type: column.DatabaseTypeName()}
		if n, ok := column.Length(); ok {
			info.Length = &n
		}
		if p, scale, ok := column.DecimalSize(); ok {
			info.Precision, info.Scale = &p, &scale
		}
		if v, ok := column.Nullable(); ok {
			info.Nullable = &v
		}
		if v, ok := column.PrimaryKey(); ok {
			info.PrimaryKey = &v
		}
		if v, ok := column.AutoIncrement(); ok {
			info.AutoIncrement = &v
		}
		if v, ok := column.DefaultValue(); ok {
			info.Default = &v
		}
		if v, ok := column.Comment(); ok {
			info.Comment = &v
		}
		s.columns = append(s.columns, info)
	}
	return expect(len(columns) > 0, "column metadata is empty")
}

func canonical(name string) string { return strings.ToUpper(strings.Trim(name, `"`)) }
