package stores

import (
	"fmt"
	"time"

	"go.sia.tech/siad/modules"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type (
	// Model defines the common fields of every table. Same as Model
	// but excludes soft deletion since it breaks cascading deletes.
	Model struct {
		ID        uint `gorm:"primarykey"`
		CreatedAt time.Time
		UpdatedAt time.Time
	}

	// SQLStore is a helper type for interacting with a SQL-based backend.
	SQLStore struct {
		db *gorm.DB
	}
)

// NewEphemeralSQLiteConnection creates a connection to an in-memory SQLite DB.
// NOTE: Use simple names such as a random hex identifier or the filepath.Base
// of a test's name. Certain symbols will break the cfg string and cause a file
// to be created on disk.
//
//	mode: set to memory for in-memory database
//	cache: set to shared which is required for in-memory databases
//	_foreign_keys: enforce foreign_key relations
func NewEphemeralSQLiteConnection(name string) gorm.Dialector {
	return sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared&_foreign_keys=1", name))
}

// NewSQLiteConnection opens a sqlite db at the given path.
//
//	_busy_timeout: set to prevent concurrent transactions from failing and
//	instead have them block
//	_foreign_keys: enforce foreign_key relations
//	_journal_mode: set to truncate instead of delete since it's usually faster
func NewSQLiteConnection(path string) gorm.Dialector {
	return sqlite.Open(fmt.Sprintf("file:%s?_busy_timeout=5000&_foreign_keys=1&_journal_mode=TRUNCATE", path))
}

// NewSQLStore uses a given Dialector to connect to a SQL database.  NOTE: Only
// pass migrate=true for the first instance of SQLHostDB if you connect via the
// same Dialector multiple times.
func NewSQLStore(conn gorm.Dialector, migrate bool) (*SQLStore, modules.ConsensusChangeID, error) {
	db, err := gorm.Open(conn, &gorm.Config{})
	if err != nil {
		return nil, modules.ConsensusChangeID{}, err
	}
	if migrate {
		// Create the tables.
		tables := []interface{}{
			// bus.ContractStore tables
			&dbArchivedContract{},
			&dbContract{},

			// bus.HostDB tables
			&dbHost{},
			&dbInteraction{},
			&dbAnnouncement{},
			&dbConsensusInfo{},

			// bus.ContractSetStore tables
			&dbContractSet{},
			&dbContractSetEntry{},

			// bus.ObjectStore tables
			&dbObject{},
			&dbSlice{},
			&dbSlab{},
			&dbSector{},
			&dbShard{},

			// bus.SettingStore tables
			&dbSetting{},
		}
		if err := db.AutoMigrate(tables...); err != nil {
			return nil, modules.ConsensusChangeID{}, err
		}
	}

	// Get latest consensus change ID or init db.
	var ci dbConsensusInfo
	err = db.Where(&dbConsensusInfo{Model: Model{ID: consensusInfoID}}).
		Attrs(dbConsensusInfo{
			Model: Model{
				ID: consensusInfoID,
			},
			CCID: modules.ConsensusChangeBeginning[:],
		}).
		FirstOrCreate(&ci).Error
	if err != nil {
		return nil, modules.ConsensusChangeID{}, err
	}
	var ccid modules.ConsensusChangeID
	copy(ccid[:], ci.CCID)

	return &SQLStore{
		db: db,
	}, ccid, nil
}

// Close closes the underlying database connection of the store.
func (s *SQLStore) Close() error {
	db, err := s.db.DB()
	if err != nil {
		return err
	}
	return db.Close()
}
