package wallet

import (
	"context"
	"fmt"
	"sync"

	"go.sia.tech/core/types"
	cwallet "go.sia.tech/coreutils/wallet"
	"go.sia.tech/renterd/alerts"
	"go.sia.tech/renterd/api"
	"go.sia.tech/renterd/autopilot/contractor"
	"go.uber.org/zap"
)

type (
	Bus interface {
		AutopilotConfig(ctx context.Context) (api.AutopilotConfig, error)

		Wallet(ctx context.Context) (api.WalletResponse, error)
		WalletPending(ctx context.Context) (resp []cwallet.Event, err error)
		WalletRedistribute(ctx context.Context, outputs int, amount types.Currency) (ids []types.TransactionID, err error)
	}

	Wallet interface {
		PerformWalletMaintenance(ctx context.Context) error
	}
)

type (
	wallet struct {
		alerter alerts.Alerter
		bus     Bus
		logger  *zap.SugaredLogger

		mu                sync.Mutex
		maintenanceTxnIDs []types.TransactionID
	}
)

func New(alerter alerts.Alerter, bus Bus, logger *zap.Logger) Wallet {
	return &wallet{
		alerter: alerter,
		bus:     bus,
		logger:  logger.Named("wallet").Sugar(),
	}
}

func (w *wallet) PerformWalletMaintenance(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.logger.Info("performing wallet maintenance")

	cfg, err := w.bus.AutopilotConfig(ctx)
	if err != nil {
		return fmt.Errorf("failed to fetch autopilot: %w", err)
	}
	wallet, err := w.bus.Wallet(ctx)
	if err != nil {
		w.logger.Warnf("wallet maintenance skipped, fetching wallet failed with err: %v", err)
		return fmt.Errorf("failed to fetch wallet: %w", err)
	}
	balance := wallet.Confirmed

	// no contracts - nothing to do
	if cfg.Contracts.Amount == 0 {
		w.logger.Warn("wallet maintenance skipped, no contracts wanted")
		return nil
	}

	// register an alert if balance is low
	if balance.Cmp(contractor.InitialContractFunding.Mul64(cfg.Contracts.Amount)) < 0 {
		if err := w.alerter.RegisterAlert(ctx, newAccountLowBalanceAlert(wallet.Address, balance, contractor.InitialContractFunding)); err != nil {
			w.logger.Warnf("failed to register low balance alert: %v", err)
		}
	} else {
		if err := w.alerter.DismissAlerts(ctx, alertLowBalanceID); err != nil {
			w.logger.Warnf("failed to dismiss low balance alert: %v", err)
		}
	}

	// pending maintenance transaction - nothing to do
	pending, err := w.bus.WalletPending(ctx)
	if err != nil {
		return nil
	}
	for _, txn := range pending {
		for _, mTxnID := range w.maintenanceTxnIDs {
			if mTxnID == types.TransactionID(txn.ID) {
				w.logger.Debugf("wallet maintenance skipped, pending transaction found with id %v", mTxnID)
				return nil
			}
		}
	}

	// figure out the amount per output
	wantedNumOutputs := 10
	amount := contractor.InitialContractFunding.Div64(uint64(wantedNumOutputs))

	// redistribute outputs
	ids, err := w.bus.WalletRedistribute(ctx, wantedNumOutputs, amount)
	if err != nil {
		return fmt.Errorf("failed to redistribute wallet into %d outputs of amount %v, balance %v, err %v", wantedNumOutputs, amount, balance, err)
	}

	w.logger.Debugf("wallet maintenance succeeded, txns %v", ids)
	w.maintenanceTxnIDs = ids
	return nil
}
