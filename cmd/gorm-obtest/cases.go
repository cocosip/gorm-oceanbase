package main

func caseDefinitions() []caseDefinition {
	cases := make([]caseDefinition, 0, 100)
	for _, field := range scalarFields() {
		cases = append(cases, scalarCase(field, false))
		// Go pointers exercise SQL NULL independently of Scanner/Valuer wrappers.
		if field.mode == modeBoth {
			cases = append(cases, scalarCase(field, true))
		}
	}
	return append(cases,
		caseDefinition{"types.empty_string", modeBoth, "empty string versus SQL NULL semantics", runEmptyString},
		caseDefinition{"types.oracle_rowid", modeOracle, "generated ROWID scan and lookup", runRowID},
		caseDefinition{"types.oracle_object", modeOracle, "named OBJECT column via GORM with exact NUMBER attributes", runObject},
		caseDefinition{"schema.foreign_key", modeBoth, "foreign key creation, metadata, enforcement, error translation and drop", runForeignKey},
		caseDefinition{"schema.check", modeBoth, "CHECK creation, metadata, enforcement and drop", runCheckConstraint},
		caseDefinition{"schema.fields", modeBoth, "nullable, NOT NULL, defaults, lengths and ignored columns", runFieldConstraints},
		caseDefinition{"schema.migration", modeBoth, "incremental/repeated AutoMigrate, rename/drop columns and data retention", runMigration},
		caseDefinition{"schema.indexes", modeBoth, "composite unique indexes, metadata, upsert and rename/drop", runIndexes},
		caseDefinition{"schema.composite_pk", modeBoth, "composite primary key and upsert matching", runCompositePrimaryKey},
		caseDefinition{"schema.mysql_comments", modeMySQL, "column comments and metadata", runComments},
		caseDefinition{"schema.identifiers", modeBoth, "custom/reserved names and Oracle explicit case", runIdentifiers},
		caseDefinition{"crud.explicit", modeBoth, "explicit-key batch, pagination, zero-value update, delete and affected rows", runCRUD},
		caseDefinition{"crud.generated_batch", modeBoth, "generated keys, expression defaults, maps and explicit NULL", runGeneratedBatch},
		caseDefinition{"crud.default_only", modeBoth, "default-only batch row count and generated key backfill", runDefaultOnly},
		caseDefinition{"crud.upsert", modeBoth, "primary-key UpdateAll and DoNothing", runUpsert},
		caseDefinition{"crud.transactions", modeBoth, "rollback, savepoint and commit", runTransaction},
		caseDefinition{"crud.associations", modeBoth, "belongs-to/has-one/has-many/many-to-many, Preload and FullSaveAssociations", runAssociations},
		caseDefinition{"crud.hooks_soft_delete", modeBoth, "hooks, automatic timestamps, ignored fields and soft delete", runHooksAndSoftDelete},
		caseDefinition{"crud.oracle_rejection", modeOracle, "reject unsafe MERGE before writes", runOracleUpsertRejection},
		caseDefinition{"crud.oracle_sequence", modeOracle, "sequence allocation and explicit key roundtrip", runOracleSequence},
	)
}
