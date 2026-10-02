package application

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"github.com/marcosarl1/backend-challenge-go/internal/domain/money"
)

// ReconciliationReport conta a conferência: saldo guardado, saldo refeito do ledger (abertura incluída), a diferença (guardado menos refeito), se bate e quantos lançamentos entraram na conta. Nunca altera o saldo.
type ReconciliationReport struct {
	WalletID       uuid.UUID
	Stored         money.Money
	Calculated     money.Money
	Difference     money.Money
	Consistent     bool
	CheckedEntries int64
}

// Reconcile refaz o saldo a partir do ledger e compara com o guardado, tudo no mesmo objeto. Em divergência, registra no log e sinaliza no relatório, sem encostar no saldo.
func Reconcile(ctx context.Context, uow UnitOfWork, logger *slog.Logger, ident Identity, walletID uuid.UUID) (*ReconciliationReport, error) {
	if err := requireInternal(ident); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	var out *ReconciliationReport
	err := uow.Do(ctx, func(ctx context.Context, r Repositories) error {
		snap, err := r.Ledger.SnapshotForReconcile(ctx, walletID)
		if err != nil {
			return err
		}
		stored, err := money.FromMinor(snap.StoredMinor, money.Currency(snap.Currency))
		if err != nil {
			return err
		}
		calculated, err := money.FromMinor(snap.Credits-snap.Debits, money.Currency(snap.Currency))
		if err != nil {
			return err
		}
		difference, err := stored.Sub(calculated)
		if err != nil {
			return err
		}
		zero, _ := money.Zero(stored.Currency())
		consistent, err := difference.Cmp(zero)
		if err != nil {
			return err
		}
		out = &ReconciliationReport{
			WalletID: walletID, Stored: stored, Calculated: calculated,
			Difference: difference, Consistent: consistent == 0,
			CheckedEntries: snap.Entries,
		}
		if !out.Consistent {
			logger.ErrorContext(ctx, "divergência na reconciliação",
				"walletId", walletID.String(),
				"consistent", false,
				"checkedEntries", snap.Entries)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("reconciliando: %w", err)
	}
	return out, nil
}
