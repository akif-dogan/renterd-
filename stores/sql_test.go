package stores

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"go.sia.tech/core/types"
	"go.sia.tech/renterd/alerts"
	"go.sia.tech/renterd/api"
	"go.sia.tech/renterd/object"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"lukechampine.com/frand"
	"moul.io/zapgorm2"
)

const (
	testPersistInterval = time.Second
	testContractSet     = "test"
	testMimeType        = "application/octet-stream"
	testETag            = "d34db33f"
)

var (
	testMetadata = api.ObjectUserMetadata{
		"foo": "bar",
		"baz": "qux",
	}
)

type testSQLStore struct {
	t *testing.T
	*SQLStore

	dbName        string
	dbMetricsName string
	dir           string
}

type testSQLStoreConfig struct {
	dbURI           string
	dbUser          string
	dbPassword      string
	dbName          string
	dbMetricsName   string
	dir             string
	persistent      bool
	skipMigrate     bool
	skipContractSet bool
}

var defaultTestSQLStoreConfig = testSQLStoreConfig{}

// newTestSQLStore creates a new SQLStore for testing.
func newTestSQLStore(t *testing.T, cfg testSQLStoreConfig) *testSQLStore {
	t.Helper()
	dir := cfg.dir
	if dir == "" {
		dir = t.TempDir()
	}

	dbURI, dbUser, dbPassword, dbName := DBConfigFromEnv()
	if dbURI == "" {
		dbURI = cfg.dbURI
	}
	if cfg.persistent && dbURI != "" {
		t.Fatal("invalid store config, can't use both persistent and dbURI")
	}
	if dbUser == "" {
		dbUser = cfg.dbUser
	}
	if dbPassword == "" {
		dbPassword = cfg.dbPassword
	}
	if dbName == "" {
		if cfg.dbName != "" {
			dbName = cfg.dbName
		} else {
			dbName = hex.EncodeToString(frand.Bytes(32)) // random name for db
		}
	}
	dbMetricsName := cfg.dbMetricsName
	if dbMetricsName == "" {
		dbMetricsName = hex.EncodeToString(frand.Bytes(32)) // random name for metrics db
	}

	var conn, connMetrics gorm.Dialector
	if dbURI != "" {
		if tmpDB, err := gorm.Open(NewMySQLConnection(dbUser, dbPassword, dbURI, "")); err != nil {
			t.Fatal(err)
		} else if err := tmpDB.Exec(fmt.Sprintf("CREATE DATABASE IF NOT EXISTS `%s`", dbName)).Error; err != nil {
			t.Fatal(err)
		} else if err := tmpDB.Exec(fmt.Sprintf("CREATE DATABASE IF NOT EXISTS `%s`", dbMetricsName)).Error; err != nil {
			t.Fatal(err)
		}

		conn = NewMySQLConnection(dbUser, dbPassword, dbURI, dbName)
		connMetrics = NewMySQLConnection(dbUser, dbPassword, dbURI, dbMetricsName)
	} else if cfg.persistent {
		conn = NewSQLiteConnection(filepath.Join(dir, "db.sqlite"))
		connMetrics = NewSQLiteConnection(filepath.Join(dir, "metrics.sqlite"))
	} else {
		conn = NewEphemeralSQLiteConnection(dbName)
		connMetrics = NewEphemeralSQLiteConnection(dbMetricsName)
	}

	walletAddrs := types.Address(frand.Entropy256())
	alerts := alerts.WithOrigin(alerts.NewManager(), "test")
	sqlStore, err := NewSQLStore(Config{
		Conn:                          conn,
		ConnMetrics:                   connMetrics,
		Alerts:                        alerts,
		PartialSlabDir:                dir,
		Migrate:                       !cfg.skipMigrate,
		AnnouncementMaxAge:            time.Hour,
		PersistInterval:               time.Second,
		WalletAddress:                 walletAddrs,
		SlabBufferCompletionThreshold: 0,
		Logger:                        zap.NewNop().Sugar(),
		GormLogger:                    newTestLogger(),
		RetryTransactionIntervals:     []time.Duration{50 * time.Millisecond, 100 * time.Millisecond, 200 * time.Millisecond},
	})
	if err != nil {
		t.Fatal("failed to create SQLStore", err)
	}
	if !cfg.skipContractSet {
		err = sqlStore.SetContractSet(context.Background(), testContractSet, []types.FileContractID{})
		if err != nil {
			t.Fatal("failed to set contract set", err)
		}
	}
	return &testSQLStore{
		SQLStore:      sqlStore,
		dbName:        dbName,
		dbMetricsName: dbMetricsName,
		dir:           dir,
		t:             t,
	}
}

func (s *testSQLStore) Close() error {
	if err := s.SQLStore.Close(); err != nil {
		s.t.Error(err)
	}
	return nil
}

func (s *testSQLStore) DefaultBucketID() uint {
	var b dbBucket
	if err := s.db.
		Model(&dbBucket{}).
		Where("name = ?", api.DefaultBucketName).
		Take(&b).
		Error; err != nil {
		s.t.Fatal(err)
	}
	return b.ID
}

func (s *testSQLStore) Reopen() *testSQLStore {
	s.t.Helper()
	cfg := defaultTestSQLStoreConfig
	cfg.dir = s.dir
	cfg.dbName = s.dbName
	cfg.dbMetricsName = s.dbMetricsName
	cfg.skipContractSet = true
	cfg.skipMigrate = true
	return newTestSQLStore(s.t, cfg)
}

func (s *testSQLStore) Retry(tries int, durationBetweenAttempts time.Duration, fn func() error) {
	s.t.Helper()
	for i := 1; i < tries; i++ {
		err := fn()
		if err == nil {
			return
		}
		time.Sleep(durationBetweenAttempts)
	}
	if err := fn(); err != nil {
		s.t.Fatal(err)
	}
}

// newTestLogger creates a console logger used for testing.
func newTestLogger() logger.Interface {
	config := zap.NewProductionEncoderConfig()
	config.EncodeTime = zapcore.RFC3339TimeEncoder
	config.EncodeLevel = zapcore.CapitalColorLevelEncoder
	config.StacktraceKey = ""
	consoleEncoder := zapcore.NewConsoleEncoder(config)

	l := zap.New(
		zapcore.NewCore(consoleEncoder, zapcore.AddSync(os.Stdout), zapcore.DebugLevel),
		zap.AddCaller(),
		zap.AddStacktrace(zapcore.ErrorLevel),
	)
	return zapgorm2.New(l)
}

func (s *testSQLStore) addTestObject(path string, o object.Object) (api.Object, error) {
	if err := s.UpdateObjectBlocking(context.Background(), api.DefaultBucketName, path, testContractSet, testETag, testMimeType, testMetadata, o); err != nil {
		return api.Object{}, err
	} else if obj, err := s.Object(context.Background(), api.DefaultBucketName, path); err != nil {
		return api.Object{}, err
	} else {
		return obj, nil
	}
}

func (s *SQLStore) addTestContracts(keys []types.PublicKey) (fcids []types.FileContractID, contracts []api.ContractMetadata, err error) {
	cnt, err := s.contractsCount()
	if err != nil {
		return nil, nil, err
	}
	for i, key := range keys {
		fcids = append(fcids, types.FileContractID{byte(int(cnt) + i + 1)})
		contract, err := s.addTestContract(fcids[len(fcids)-1], key)
		if err != nil {
			return nil, nil, err
		}
		contracts = append(contracts, contract)
	}
	return
}

func (s *SQLStore) addTestContract(fcid types.FileContractID, hk types.PublicKey) (api.ContractMetadata, error) {
	rev := testContractRevision(fcid, hk)
	return s.AddContract(context.Background(), rev, types.ZeroCurrency, types.ZeroCurrency, 0, api.ContractStatePending)
}

func (s *SQLStore) addTestRenewedContract(fcid, renewedFrom types.FileContractID, hk types.PublicKey, startHeight uint64) (api.ContractMetadata, error) {
	rev := testContractRevision(fcid, hk)
	return s.AddRenewedContract(context.Background(), rev, types.ZeroCurrency, types.ZeroCurrency, startHeight, renewedFrom, api.ContractStatePending)
}

func (s *SQLStore) contractsCount() (cnt int64, err error) {
	err = s.db.
		Model(&dbContract{}).
		Count(&cnt).
		Error
	return
}

func (s *SQLStore) overrideSlabHealth(objectID string, health float64) (err error) {
	err = s.db.Exec(fmt.Sprintf(`
	UPDATE slabs SET health = %v WHERE id IN (
		SELECT * FROM (
			SELECT sla.id
			FROM objects o
			INNER JOIN slices sli ON o.id = sli.db_object_id
			INNER JOIN slabs sla ON sli.db_slab_id = sla.id
			WHERE o.object_id = "%s"
		) AS sub
	)`, health, objectID)).Error
	return
}

type sqliteQueryPlan struct {
	Detail string `json:"detail"`
}

func (p sqliteQueryPlan) usesIndex() bool {
	d := strings.ToLower(p.Detail)
	return strings.Contains(d, "using index") || strings.Contains(d, "using covering index")
}

//nolint:tagliatelle
type mysqlQueryPlan struct {
	Extra        string `json:"Extra"`
	PossibleKeys string `json:"possible_keys"`
}

func (p mysqlQueryPlan) usesIndex() bool {
	d := strings.ToLower(p.Extra)
	return strings.Contains(d, "using index") || strings.Contains(p.PossibleKeys, "idx_")
}

func TestQueryPlan(t *testing.T) {
	ss := newTestSQLStore(t, defaultTestSQLStoreConfig)
	defer ss.Close()

	queries := []string{
		// allow_list
		"SELECT * FROM host_allowlist_entry_hosts WHERE db_host_id = 1",
		"SELECT * FROM host_allowlist_entry_hosts WHERE db_allowlist_entry_id = 1",

		// block_list
		"SELECT * FROM host_blocklist_entry_hosts WHERE db_host_id = 1",
		"SELECT * FROM host_blocklist_entry_hosts WHERE db_blocklist_entry_id = 1",

		// contract_sectors
		"SELECT * FROM contract_sectors WHERE db_contract_id = 1",
		"SELECT * FROM contract_sectors WHERE db_sector_id = 1",
		"SELECT COUNT(DISTINCT db_sector_id) FROM contract_sectors",

		// contract_set_contracts
		"SELECT * FROM contract_set_contracts WHERE db_contract_id = 1",
		"SELECT * FROM contract_set_contracts WHERE db_contract_set_id = 1",

		// slabs
		"SELECT * FROM slabs WHERE health_valid_until > 0",
		"SELECT * FROM slabs WHERE health > 0",
		"SELECT * FROM slabs WHERE db_buffered_slab_id = 1",

		// objects
		"SELECT * FROM objects WHERE db_bucket_id = 1",
		"SELECT * FROM objects WHERE etag = ''",
	}

	for _, query := range queries {
		if isSQLite(ss.db) {
			var explain sqliteQueryPlan
			if err := ss.db.Raw(fmt.Sprintf("EXPLAIN QUERY PLAN %s;", query)).Scan(&explain).Error; err != nil {
				t.Fatal(err)
			} else if !explain.usesIndex() {
				t.Fatalf("query '%s' should use an index, instead the plan was %+v", query, explain)
			}
		} else {
			var explain mysqlQueryPlan
			if err := ss.db.Raw(fmt.Sprintf("EXPLAIN %s;", query)).Scan(&explain).Error; err != nil {
				t.Fatal(err)
			} else if !explain.usesIndex() {
				t.Fatalf("query '%s' should use an index, instead the plan was %+v", query, explain)
			}
		}
	}
}

func TestRetryTransaction(t *testing.T) {
	ss := newTestSQLStore(t, defaultTestSQLStoreConfig)
	defer ss.Close()

	// create custom logger to capture logs
	observedZapCore, observedLogs := observer.New(zap.InfoLevel)
	ss.logger = zap.New(observedZapCore).Sugar()

	// collectLogs returns all logs
	collectLogs := func() (logs []string) {
		t.Helper()
		for _, entry := range observedLogs.All() {
			logs = append(logs, entry.Message)
		}
		return
	}

	// disable retries and retry a transaction that fails
	ss.retryTransactionIntervals = nil
	ss.retryTransaction(context.Background(), func(tx *gorm.DB) error { return errors.New("database locked") })

	// assert transaction is attempted once and not retried
	got := collectLogs()
	want := []string{"transaction attempt 1/1 failed, err: database locked"}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("unexpected logs", cmp.Diff(got, want))
	}

	// enable retries and retry the same transaction
	ss.retryTransactionIntervals = []time.Duration{
		5 * time.Millisecond,
		10 * time.Millisecond,
		15 * time.Millisecond,
	}
	ss.retryTransaction(context.Background(), func(tx *gorm.DB) error { return errors.New("database locked") })

	// assert transaction is retried 4 times in total
	got = collectLogs()
	want = append(want,
		"transaction attempt 1/4 failed, retry in 5ms,  err: database locked",
		"transaction attempt 2/4 failed, retry in 10ms,  err: database locked",
		"transaction attempt 3/4 failed, retry in 15ms,  err: database locked",
		"transaction attempt 4/4 failed, err: database locked",
	)
	if !reflect.DeepEqual(got, want) {
		t.Fatal("unexpected logs", cmp.Diff(got, want))
	}

	// retry transaction with cancelled context
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ss.retryTransaction(ctx, func(tx *gorm.DB) error { return nil })
	if len(observedLogs.All()) != len(want) {
		t.Fatal("expected no logs")
	}

	ctx, cancel = context.WithTimeout(context.Background(), 1*time.Microsecond)
	defer cancel()
	time.Sleep(time.Millisecond)
	ss.retryTransaction(ctx, func(tx *gorm.DB) error { return nil })
	if len(observedLogs.All()) != len(want) {
		t.Fatal("expected no logs")
	}
}
