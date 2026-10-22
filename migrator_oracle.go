package oceanbase

import (
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/migrator"
)

// oracleMigrator implements gorm.Migrator for Oracle compatibility mode.
// Schema, column and index lookups run against the Oracle data dictionary
// (ALL_TAB_COLUMNS / ALL_CONSTRAINTS / ALL_INDEXES); those queries arrive with
// the Oracle migrator task. Until then the embedded generic migrator serves
// the interface.
type oracleMigrator struct {
	migrator.Migrator
	dialector *Dialector
}

func newOracleMigrator(db *gorm.DB, dialector *Dialector) gorm.Migrator {
	return &oracleMigrator{
		Migrator: migrator.Migrator{
			Config: migrator.Config{DB: db, Dialector: dialector},
		},
		dialector: dialector,
	}
}

// OracleSequenceMigrator is the Oracle sequence extension of the GORM
// migrator. It is not part of the generic gorm.Migrator interface; reach it
// with a type assertion on db.Migrator().
type OracleSequenceMigrator interface {
	// CreateSequence creates a named sequence in the current schema; the
	// identifier is quoted and uppercased like every unquoted identifier.
	CreateSequence(name string) error
	// HasSequence reports whether the sequence exists in the current schema.
	HasSequence(name string) bool
}

// CreateSequence implements OracleSequenceMigrator. Values are drawn in the
// caller's transaction with <name>.NEXTVAL before the insert (design.md §6.2).
func (m *oracleMigrator) CreateSequence(name string) error {
	return m.DB.Exec("CREATE SEQUENCE ?", clause.Column{Name: name}).Error
}

// HasSequence implements OracleSequenceMigrator. Unquoted identifiers fold to
// upper case, so the lookup compares the folded name.
func (m *oracleMigrator) HasSequence(name string) bool {
	var count int64
	_ = m.DB.Raw("SELECT count(*) FROM user_sequences WHERE sequence_name = ?", strings.ToUpper(name)).
		Row().Scan(&count)
	return count > 0
}

var (
	_ gorm.Migrator          = (*oracleMigrator)(nil)
	_ OracleSequenceMigrator = (*oracleMigrator)(nil)
)
