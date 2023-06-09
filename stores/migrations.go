package stores

import (
	"context"
	"fmt"

	"gorm.io/gorm"
	glogger "gorm.io/gorm/logger"
)

type dbHostBlocklistEntryHost struct {
	DBBlocklistEntryID uint8 `gorm:"primarykey;column:db_blocklist_entry_id"`
	DBHostID           uint8 `gorm:"primarykey;index:idx_db_host_id;column:db_host_id"`
}

func (dbHostBlocklistEntryHost) TableName() string {
	return "host_blocklist_entry_hosts"
}

// migrateShards performs the migrations necessary for removing the 'shards'
// table.
func migrateShards(ctx context.Context, db *gorm.DB, logger glogger.Interface) error {
	m := db.Migrator()

	// add columns
	if !m.HasColumn(&dbSlice{}, "db_slab_id") {
		logger.Info(ctx, "adding column db_slab_id to table 'slices'")
		if err := m.AddColumn(&dbSlice{}, "db_slab_id"); err != nil {
			return err
		}
		logger.Info(ctx, "done adding column db_slab_id to table 'slices'")
	}
	if !m.HasColumn(&dbSector{}, "db_slab_id") {
		logger.Info(ctx, "adding column db_slab_id to table 'sectors'")
		if err := m.AddColumn(&dbSector{}, "db_slab_id"); err != nil {
			return err
		}
		logger.Info(ctx, "done adding column db_slab_id to table 'sectors'")
	}

	// populate new columns
	if m.HasColumn(&dbSlab{}, "db_slice_id") {
		logger.Info(ctx, "populating column 'db_slab_id' in table 'slices'")
		if err := db.Exec(`UPDATE slices sli
		INNER JOIN slabs sla ON sli.id=sla.db_slice_id
		SET sli.db_slab_id=sla.id`).Error; err != nil {
			return err
		}
		logger.Info(ctx, "done populating column 'db_slab_id' in table 'slices'")
	}
	logger.Info(ctx, "populating column 'db_slab_id' in table 'sectors'")
	if err := db.Exec(`UPDATE sectors sec
		INNER JOIN shards sha ON sec.id=sha.db_sector_id
		SET sec.db_slab_id=sha.db_slab_id`).Error; err != nil {
		return err
	}
	logger.Info(ctx, "done populating column 'db_slab_id' in table 'sectors'")

	// drop column db_slice_id from slabs
	logger.Info(ctx, "dropping constraint 'fk_slices_slab' from table 'slabs'")
	if err := m.DropConstraint(&dbSlab{}, "fk_slices_slab"); err != nil {
		return err
	}
	logger.Info(ctx, "done dropping constraint 'fk_slices_slab' from table 'slabs'")
	logger.Info(ctx, "dropping column 'db_slice_id' from table 'slabs'")
	if err := m.DropColumn(&dbSlab{}, "db_slice_id"); err != nil {
		return err
	}
	logger.Info(ctx, "done dropping column 'db_slice_id' from table 'slabs'")

	// delete any sectors that are not referenced by a slab
	logger.Info(ctx, "pruning dangling sectors")
	if err := db.Exec(`DELETE FROM sectors WHERE db_slab_id IS NULL`).Error; err != nil {
		return err
	}
	logger.Info(ctx, "done pruning dangling sectors")

	// drop table shards
	logger.Info(ctx, "dropping table 'shards'")
	if err := m.DropTable("shards"); err != nil {
		return err
	}
	logger.Info(ctx, "done dropping table 'shards'")
	return nil
}

func performMigrations(db *gorm.DB, logger glogger.Interface) error {
	ctx := context.Background()
	m := db.Migrator()

	// Perform pre-auto migrations
	//
	// If the consensus info table is missing the height column, drop it to
	// force a resync.
	if m.HasTable(&dbConsensusInfo{}) && !m.HasColumn(&dbConsensusInfo{}, "height") {
		if err := m.DropTable(&dbConsensusInfo{}); err != nil {
			return err
		}
	}
	// If the shards table exists, we add the db_slab_id column to slices and
	// sectors before then dropping the shards table as well as the db_slice_id
	// column from the slabs table.
	if m.HasTable("shards") {
		logger.Info(ctx, "'shards' table detected, starting migration")
		if err := migrateShards(ctx, db, logger); err != nil {
			return fmt.Errorf("failed to migrate 'shards' table: %w", err)
		}
		logger.Info(ctx, "finished migrating 'shards' table")
	}

	// Perform auto migrations.
	tables := []interface{}{
		// bus.MetadataStore tables
		&dbArchivedContract{},
		&dbContract{},
		&dbContractSet{},
		&dbObject{},
		&dbSlab{},
		&dbSector{},
		&dbSlice{},

		// bus.HostDB tables
		&dbAnnouncement{},
		&dbConsensusInfo{},
		&dbHost{},
		&dbInteraction{},
		&dbAllowlistEntry{},
		&dbBlocklistEntry{},

		// wallet tables
		&dbSiacoinElement{},
		&dbTransaction{},

		// bus.SettingStore tables
		&dbSetting{},

		// bus.EphemeralAccountStore tables
		&dbAccount{},
	}
	if err := db.AutoMigrate(tables...); err != nil {
		return err
	}

	// Perform post-auto migrations.
	if err := m.DropTable("host_sectors"); err != nil {
		return err
	}
	if !m.HasIndex(&dbHostBlocklistEntryHost{}, "DBHostID") {
		if err := m.CreateIndex(&dbHostBlocklistEntryHost{}, "DBHostID"); err != nil {
			return err
		}
	}
	return nil
}
