package mysql

import (
	dsql "database/sql"
	"embed"
	"errors"
	"fmt"

	"go.sia.tech/renterd/internal/sql"
	"go.uber.org/zap"
)

//go:embed all:migrations/*
var migrationsFs embed.FS

func performMigrations(db *sql.DB, identifier string, migrations []sql.Migration, l *zap.SugaredLogger) error {
	// check if the migrations table exists
	var dummy string
	if err := db.QueryRow("SHOW TABLES LIKE 'migrations'").Scan(&dummy); err != nil && !errors.Is(err, dsql.ErrNoRows) {
		return fmt.Errorf("failed to check for migrations table: %w", err)
	}
	if dummy == "" {
		// init schema if it doesn't
		return initSchema(db, identifier, migrations, l)
	}

	// check if the migrations table is empty
	var isEmpty bool
	if err := db.QueryRow("SELECT COUNT(*) = 0 FROM migrations").Scan(&isEmpty); err != nil {
		return fmt.Errorf("failed to count rows in migrations table: %w", err)
	} else if isEmpty {
		// table is empty, init schema
		return initSchema(db, identifier, migrations, l)
	}

	// check if the schema was initialised already
	var initialised bool
	if err := db.QueryRow("SELECT EXISTS (SELECT 1 FROM migrations WHERE id = ?)", sql.SCHEMA_INIT).Scan(&initialised); err != nil {
		return fmt.Errorf("failed to check if schema was initialised: %w", err)
	} else if !initialised {
		return fmt.Errorf("schema was not initialised but has a non-empty migration table")
	}

	// apply missing migrations
	for _, migration := range migrations {
		if err := db.Transaction(func(tx sql.Tx) error {
			// check if migration was already applied
			var applied bool
			if err := tx.QueryRow("SELECT EXISTS (SELECT 1 FROM migrations WHERE id = ?)", migration.ID).Scan(&applied); err != nil {
				return fmt.Errorf("failed to check if migration '%s' was already applied: %w", migration.ID, err)
			} else if applied {
				return nil
			}

			// run migration
			return migration.Migrate(tx)
		}); err != nil {
			return fmt.Errorf("migration '%s' failed: %w", migration.ID, err)
		}
	}
	return nil
}

// initSchema is executed only on a clean database. Otherwise the individual
// migrations are executed.
func initSchema(db *sql.DB, identifier string, migrations []sql.Migration, logger *zap.SugaredLogger) error {
	return db.Transaction(func(tx sql.Tx) error {
		logger.Infof("initializing '%s' schema", identifier)

		// create migrations table if necessary
		if _, err := tx.Exec(`
			CREATE TABLE migrations (
				id varchar(255) NOT NULL,
				PRIMARY KEY (id)
			) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;`); err != nil {
			return fmt.Errorf("failed to create migrations table: %w", err)
		}
		// insert SCHEMA_INIT
		if _, err := tx.Exec("INSERT INTO migrations (id) VALUES (?)", sql.SCHEMA_INIT); err != nil {
			return fmt.Errorf("failed to insert SCHEMA_INIT: %w", err)
		}
		// insert migration ids
		for _, migration := range migrations {
			if _, err := tx.Exec("INSERT INTO migrations (id) VALUES (?)", migration.ID); err != nil {
				return fmt.Errorf("failed to insert migration '%s': %w", migration.ID, err)
			}
		}
		// create remaining schema
		if err := sql.ExecSQLFile(tx, migrationsFs, identifier, "schema"); err != nil {
			return fmt.Errorf("failed to execute schema: %w", err)
		}

		logger.Infof("initialization complete")
		return nil
	})
}

func version(db *sql.DB) (string, string, error) {
	var version string
	if err := db.QueryRow("select version()").Scan(&version); err != nil {
		return "", "", err
	}
	return "MySQL", version, nil
}
