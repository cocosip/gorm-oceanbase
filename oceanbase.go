package oceanbase

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	ob "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/dberrors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

const (
	// DefaultDriverName is the registered database/sql driver name.
	DefaultDriverName = "oceanbase"

	// mysqlDatetimePrecision is the default datetime precision of MySQL mode,
	// aligned with the gorm mysql driver.
	mysqlDatetimePrecision = 3
	// oracleDatetimePrecision matches the Oracle TIMESTAMP default precision.
	oracleDatetimePrecision = 6

	// mysqlErrFunctionDoesNotExist is ER_SP_DOES_NOT_EXIST; it is the only
	// version-query failure that may fall back from OB_VERSION() to VERSION().
	mysqlErrFunctionDoesNotExist = 1305
)

// Configuration errors returned by Initialize. The input is never silently
// corrected, because a wrong mode or time policy changes query semantics.
var (
	// ErrInvalidConfiguration reports an unusable Config combination.
	ErrInvalidConfiguration = errors.New("oceanbase: invalid configuration")
	// ErrUnsupportedMode reports a mode this version of the dialect cannot serve.
	ErrUnsupportedMode = errors.New("oceanbase: unsupported compatibility mode")
	// ErrUpsertUnsupported reports an upsert request the Oracle create callback
	// cannot serve; the semantics are never silently dropped (design.md §5.3).
	ErrUpsertUnsupported = errors.New("oceanbase: upsert is not supported in Oracle mode")
)

// Config configures the OceanBase dialector. DSN, DSNConfig and Conn are
// mutually exclusive; exactly one connection source is required.
type Config struct {
	// DSN is an oceanbase:// URL parsed by the driver. Used alone.
	DSN string
	// DSNConfig is a structured driver config. It is copied, never serialized
	// back to a DSN, and the caller's value is not modified.
	DSNConfig *ob.Config
	// Conn is a caller-owned connection pool. It must already use the selected
	// concrete mode and ParseTime; the dialect never modifies or closes it.
	Conn gorm.ConnPool

	// Mode overrides the mode from the DSN or DSNConfig. The zero value means
	// "not specified", which defaults to MySQL. Automatic detection is requested
	// with the DSN parameter mode=auto only.
	Mode ob.Mode

	// SkipInitializeWithVersion skips the dialect version query. Unknown
	// versions keep version-gated features disabled; mode resolution still runs.
	SkipInitializeWithVersion bool
	// ServerVersion is a known OceanBase product version. When set the version
	// query is skipped.
	ServerVersion string

	// DefaultStringSize is the fallback length for a string column. Zero keeps
	// the gorm mysql v1.6 behaviour (longtext, or varchar(191) for indexed,
	// primary and default-valued fields).
	DefaultStringSize uint
	// DefaultDatetimePrecision is the datetime precision used when a field does
	// not specify one. Nil defaults to 3.
	DefaultDatetimePrecision *int
	// DisableDatetimePrecision omits datetime precision entirely.
	DisableDatetimePrecision bool
	// CompatibleMysqlVersion selects the MySQL client profile (5 or 8). Zero
	// means: DSN explicit value, then DSNConfig non-zero value, then 8.
	CompatibleMysqlVersion int

	// DontSupportRenameIndex disables ALTER TABLE ... RENAME INDEX.
	DontSupportRenameIndex bool
	// DontSupportRenameColumn disables ALTER TABLE ... RENAME COLUMN.
	DontSupportRenameColumn bool
	// DontSupportForShareClause rewrites FOR SHARE to LOCK IN SHARE MODE.
	DontSupportForShareClause bool
	// DontSupportDropConstraint disables standard DROP CONSTRAINT syntax.
	DontSupportDropConstraint bool
	// DontSupportNullAsDefaultValue treats a NULL default as no default.
	DontSupportNullAsDefaultValue bool
	// DontSupportRenameColumnUnique strips UNIQUE when altering a column type.
	DontSupportRenameColumnUnique bool

	// resolvedMode is the concrete mode pinned for this dialector.
	resolvedMode ob.Mode
	// defaultNowFunc records that the user did not supply a NowFunc.
	defaultNowFunc bool
}

// Dialector is the GORM dialector for OceanBase.
type Dialector struct {
	*Config
}

// Open builds a dialector from an oceanbase:// DSN. It never connects;
// parsing and configuration errors surface from Initialize.
func Open(dsn string) gorm.Dialector {
	return &Dialector{Config: &Config{DSN: dsn}}
}

// New builds a dialector from a Config. It never connects and copies the
// caller's DSNConfig pointer contents when initializing.
func New(config Config) gorm.Dialector {
	return &Dialector{Config: &config}
}

// Name implements gorm.Dialector.
func (d *Dialector) Name() string { return DefaultDriverName }

// Apply records whether the user supplied a NowFunc. Precision cannot be
// installed yet because an auto-probed mode is still unknown.
func (d *Dialector) Apply(config *gorm.Config) error {
	d.defaultNowFunc = config.NowFunc == nil
	return nil
}

// nowFunc returns the timestamp precision policy for the pinned mode: MySQL
// truncates to the configured datetime precision (default milliseconds),
// Oracle truncates to microseconds (TIMESTAMP default precision 6).
func (d *Dialector) nowFunc() func() time.Time {
	precision := mysqlDatetimePrecision
	if d.mode() == ob.ModeOracle {
		precision = oracleDatetimePrecision
	} else if d.DefaultDatetimePrecision != nil {
		precision = *d.DefaultDatetimePrecision
	}
	step := time.Second
	for i := 0; i < precision; i++ {
		step /= 10
	}
	return func() time.Time { return time.Now().Round(step) }
}

// BindVarTo implements gorm.Dialector. The driver accepts positional
// parameters in both compatibility modes.
func (d *Dialector) BindVarTo(writer clause.Writer, _ *gorm.Statement, _ interface{}) {
	writeByte(writer, '?')
}

// Explain implements gorm.Dialector.
func (d *Dialector) Explain(sql string, vars ...interface{}) string {
	return logger.ExplainSQL(sql, nil, `'`, vars...)
}

// SavePoint implements gorm.SavePointerDialectorInterface.
func (d *Dialector) SavePoint(tx *gorm.DB, name string) error {
	return tx.Exec("SAVEPOINT " + name).Error
}

// RollbackTo implements gorm.SavePointerDialectorInterface.
func (d *Dialector) RollbackTo(tx *gorm.DB, name string) error {
	return tx.Exec("ROLLBACK TO SAVEPOINT " + name).Error
}

// Initialize implements gorm.Dialector. Every failing step runs against a
// candidate pool that is only published to db.ConnPool after success, so GORM
// can never close a caller-owned pool on the error path.
func (d *Dialector) Initialize(db *gorm.DB) error {
	driverCfg, err := d.resolveDriverConfig()
	if err != nil {
		return err
	}

	var (
		candidate gorm.ConnPool
		owned     *sql.DB
	)
	if d.Conn != nil {
		candidate = d.Conn
	} else {
		connector, connectorErr := ob.NewConnector(*driverCfg)
		if connectorErr != nil {
			return connectorErr
		}
		owned = sql.OpenDB(connector)
		candidate = owned
	}

	serverVersion, versionErr := d.probeVersion(candidate)
	if versionErr != nil {
		if owned != nil {
			_ = owned.Close()
		}
		return versionErr
	}
	d.ServerVersion = serverVersion
	d.applySyntaxGates()

	if d.defaultNowFunc {
		db.NowFunc = d.nowFunc()
	}

	d.registerCallbacks(db)
	for name, builder := range d.clauseBuilders() {
		if _, exists := db.ClauseBuilders[name]; !exists {
			db.ClauseBuilders[name] = builder
		}
	}

	db.ConnPool = candidate
	return nil
}

// resolveDriverConfig validates the inputs, resolves the mode (including an
// auto probe) and returns the final structured driver config.
func (d *Dialector) resolveDriverConfig() (*ob.Config, error) {
	if err := d.validateConnectionSources(); err != nil {
		return nil, err
	}

	var (
		driverCfg          ob.Config
		dsnModeExplicit    bool
		dsnParseTimeSet    bool
		dsnProfileExplicit bool
	)
	switch {
	case d.DSN != "":
		parsed, err := ob.ParseDSN(d.DSN)
		if err != nil {
			return nil, err
		}
		driverCfg = parsed
		dsnModeExplicit = dsnHasParam(d.DSN, "mode")
		dsnParseTimeSet = dsnHasParam(d.DSN, "parseTime")
		dsnProfileExplicit = dsnHasParam(d.DSN, "compatibleMysqlVersion")
	case d.DSNConfig != nil:
		driverCfg = *d.DSNConfig
	default:
		// External pool: the caller owns ParseTime and the MySQL profile.
		driverCfg = ob.Config{}
	}

	// A GORM time.Time field needs the driver's ParseTime conversion. An
	// explicit opt-out is a configuration error, never silently overridden.
	if dsnParseTimeSet && !driverCfg.Session.ParseTime {
		return nil, fmt.Errorf("%w: DSN parseTime=false cannot be used by GORM", ErrInvalidConfiguration)
	}

	mode, err := d.resolveMode(driverCfg, dsnModeExplicit)
	if err != nil {
		return nil, err
	}

	driverCfg.Mode = mode
	d.resolvedMode = mode
	if d.Conn != nil {
		// External pool: the caller owns ParseTime, the MySQL profile and every
		// session option, so only the concrete mode is recorded here.
		return &driverCfg, nil
	}

	driverCfg.Session.ParseTime = true
	if mode == ob.ModeMySQL {
		// The MySQL client profile is meaningless for an Oracle tenant.
		driverCfg.Session.CompatibleMysqlVersion = d.resolveMySQLProfile(driverCfg, dsnProfileExplicit)
	}
	return &driverCfg, nil
}

func (d *Dialector) validateConnectionSources() error {
	sources := 0
	if d.DSN != "" {
		sources++
	}
	if d.DSNConfig != nil {
		sources++
	}
	if d.Conn != nil {
		sources++
	}
	switch sources {
	case 1:
	case 0:
		return fmt.Errorf("%w: one of DSN, DSNConfig or Conn is required", ErrInvalidConfiguration)
	default:
		return fmt.Errorf("%w: DSN, DSNConfig and Conn are mutually exclusive", ErrInvalidConfiguration)
	}
	if d.Conn != nil && d.Mode != ob.ModeMySQL && d.Mode != ob.ModeOracle {
		return fmt.Errorf("%w: an external Conn requires an explicit concrete Mode", ErrInvalidConfiguration)
	}
	return nil
}

// resolveMode applies the documented precedence:
// Config.Mode > DSN explicit mode / DSNConfig non-zero mode > MySQL.
func (d *Dialector) resolveMode(driverCfg ob.Config, dsnModeExplicit bool) (ob.Mode, error) {
	if d.Mode != 0 {
		if d.Mode != ob.ModeMySQL && d.Mode != ob.ModeOracle {
			return 0, fmt.Errorf("%w: unknown Mode", ErrInvalidConfiguration)
		}
		return d.Mode, nil
	}
	if d.Conn != nil {
		return 0, fmt.Errorf("%w: an external Conn requires an explicit concrete Mode", ErrInvalidConfiguration)
	}
	if dsnModeExplicit {
		if driverCfg.Mode != ob.ModeAuto {
			return driverCfg.Mode, nil
		}
		return d.probeMode()
	}
	if d.DSNConfig != nil && d.DSNConfig.Mode != ob.ModeAuto {
		return d.DSNConfig.Mode, nil
	}
	return ob.ModeMySQL, nil
}

// probeMode resolves mode=auto by reading ob_compatibility_mode over a
// short-lived connection. The temporary pool is closed before the real
// connector is built and never executes ORM statements.
func (d *Dialector) probeMode() (ob.Mode, error) {
	probeCfg, err := d.probeConfig()
	if err != nil {
		return 0, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	value, err := sessionVariableReader(ctx, probeCfg)
	if err != nil {
		return 0, fmt.Errorf("%w: compatibility mode probe: %w", ErrInvalidConfiguration, err)
	}
	return parseCompatibilityMode(value)
}

// probeConfig rebuilds the driver config for the probe so session
// initialization matches the real connector.
func (d *Dialector) probeConfig() (ob.Config, error) {
	var probeCfg ob.Config
	switch {
	case d.DSN != "":
		parsed, err := ob.ParseDSN(d.DSN)
		if err != nil {
			return ob.Config{}, err
		}
		probeCfg = parsed
	case d.DSNConfig != nil:
		probeCfg = *d.DSNConfig
	}
	probeCfg.Mode = ob.ModeAuto
	probeCfg.Session.ParseTime = true
	return probeCfg, nil
}

// readCompatibilityMode opens a temporary pool, reads one session variable and
// releases both connection and pool before returning.
func readCompatibilityMode(ctx context.Context, cfg ob.Config) (sql.NullString, error) {
	connector, err := ob.NewConnector(cfg)
	if err != nil {
		return sql.NullString{}, err
	}
	pool := sql.OpenDB(connector)
	defer func() { _ = pool.Close() }()

	conn, err := pool.Conn(ctx)
	if err != nil {
		return sql.NullString{}, err
	}
	defer func() { _ = conn.Close() }()
	return ob.SessionVariable(ctx, conn, "ob_compatibility_mode")
}

// sessionVariableReader is the indirection probeMode uses to read
// ob_compatibility_mode. It is a variable so protocol tests can stand in for
// the server without a network endpoint.
var sessionVariableReader = readCompatibilityMode

// parseCompatibilityMode accepts only a non-NULL MYSQL or ORACLE value,
// ignoring case. Anything else is an initialization error.
func parseCompatibilityMode(value sql.NullString) (ob.Mode, error) {
	if !value.Valid {
		return 0, fmt.Errorf("%w: ob_compatibility_mode is NULL", ErrInvalidConfiguration)
	}
	switch strings.ToUpper(strings.TrimSpace(value.String)) {
	case "MYSQL":
		return ob.ModeMySQL, nil
	case "ORACLE":
		return ob.ModeOracle, nil
	default:
		return 0, fmt.Errorf("%w: unknown compatibility mode %q", ErrInvalidConfiguration, value.String)
	}
}

// probeTimeout bounds the automatic mode detection.
const probeTimeout = 30 * time.Second

// resolveMySQLProfile applies: top-level value > DSN explicit value >
// DSNConfig non-zero value > 8. A normalized DSNConfig that already holds 5 is
// kept because its origin cannot be known.
func (d *Dialector) resolveMySQLProfile(driverCfg ob.Config, dsnProfileExplicit bool) int {
	profile := d.CompatibleMysqlVersion
	if profile == 0 && dsnProfileExplicit {
		profile = driverCfg.Session.CompatibleMysqlVersion
	}
	if profile == 0 && d.DSNConfig != nil {
		profile = d.DSNConfig.Session.CompatibleMysqlVersion
	}
	if profile == 0 {
		profile = 8
	}
	return profile
}

// mode returns the compatibility mode pinned for this dialector.
func (d *Dialector) mode() ob.Mode { return d.resolvedMode }

// applySyntaxGates enables the MySQL 5.7-compatible syntax gates unless the
// caller already chose them. A user setting is never turned back off. Oracle
// mode has its own SQL surface and carries no MySQL gate.
func (d *Dialector) applySyntaxGates() {
	if d.mode() != ob.ModeMySQL {
		return
	}
	gates := mysqlCapabilities()
	d.DontSupportRenameColumn = d.DontSupportRenameColumn || gates.dontSupportRenameColumn
	d.DontSupportForShareClause = d.DontSupportForShareClause || gates.dontSupportForShareClause
	d.DontSupportDropConstraint = d.DontSupportDropConstraint || gates.dontSupportDropConstraint
}

// probeVersion resolves the OceanBase product version, honouring an explicit
// ServerVersion and the skip flag. Each mode asks for the real OB version in
// its own SQL dialect; only the MySQL query may fall back to VERSION().
func (d *Dialector) probeVersion(pool gorm.ConnPool) (string, error) {
	if d.ServerVersion != "" {
		return d.ServerVersion, nil
	}
	if d.SkipInitializeWithVersion {
		return "", nil
	}

	if d.mode() == ob.ModeOracle {
		var raw string
		if err := pool.QueryRowContext(context.Background(), "SELECT ob_version() FROM dual").Scan(&raw); err != nil {
			return "", err
		}
		return raw, nil
	}

	var raw string
	err := pool.QueryRowContext(context.Background(), "SELECT OB_VERSION()").Scan(&raw)
	if err != nil && isUnsupportedFunction(err) {
		if fallbackErr := pool.QueryRowContext(context.Background(), "SELECT VERSION()").Scan(&raw); fallbackErr != nil {
			return "", fallbackErr
		}
		return raw, nil
	}
	if err != nil {
		return "", err
	}
	return raw, nil
}

// isUnsupportedFunction reports the MySQL "function does not exist" error that
// OB_VERSION() may raise on servers without the function. Network, permission
// and syntax failures are not masked by the VERSION() fallback.
func isUnsupportedFunction(err error) bool {
	var serverErr *dberrors.Error
	return errors.As(err, &serverErr) && serverErr.Code == mysqlErrFunctionDoesNotExist
}

// dsnHasParam reports whether the DSN query string contains name. It is used
// before ParseDSN applies driver defaults, so explicit and defaulted values can
// be told apart.
func dsnHasParam(dsn, name string) bool {
	_, query, ok := strings.Cut(dsn, "?")
	if !ok {
		return false
	}
	values, err := url.ParseQuery(query)
	if err != nil {
		return false
	}
	return values.Has(name)
}

// registerCallbacks installs the mode-specific callback set. User-registered
// clauses and builders are preserved by GORM.
func (d *Dialector) registerCallbacks(db *gorm.DB) {
	if d.mode() == ob.ModeOracle {
		registerOracleCallbacks(db)
		return
	}
	registerMySQLCallbacks(db)
}

// clauseBuilders returns the mode-specific clause rewrites.
func (d *Dialector) clauseBuilders() map[string]clause.ClauseBuilder {
	if d.mode() == ob.ModeOracle {
		return oracleClauseBuilders(d)
	}
	return mysqlClauseBuilders(d)
}

// Migrator implements gorm.Dialector.
func (d *Dialector) Migrator(db *gorm.DB) gorm.Migrator {
	if d.mode() == ob.ModeOracle {
		return newOracleMigrator(db, d)
	}
	return newMySQLMigrator(db, d)
}

var (
	_ gorm.Dialector                     = (*Dialector)(nil)
	_ gorm.SavePointerDialectorInterface = (*Dialector)(nil)
	_ gorm.ErrorTranslator               = (*Dialector)(nil)
)
