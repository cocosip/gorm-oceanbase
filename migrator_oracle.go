package oceanbase

import (
	"gorm.io/gorm"
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

var _ gorm.Migrator = (*oracleMigrator)(nil)
