package events

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/marcosarl1/backend-challenge-go/internal/domain/money"
)

func mustUUID(t *testing.T, s string) uuid.UUID {
	t.Helper()
	id, err := uuid.Parse(s)
	if err != nil {
		t.Fatalf("uuid inválido: %v", err)
	}
	return id
}

func mustMoney(t *testing.T, amount string) money.Money {
	t.Helper()
	m, err := money.Parse(amount, "BRL")
	if err != nil {
		t.Fatalf("Parse(%q) erro inesperado: %v", amount, err)
	}
	return m
}

func fixtureTime() time.Time {
	return time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
}

// checkGolden compara o envelope com o golden file.
func checkGolden(t *testing.T, name string, envelope any) {
	t.Helper()
	data, err := json.MarshalIndent(envelope, "", "  ")
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	data = append(data, '\n')
	path := filepath.Join("testdata", name+".golden.json")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatalf("erro inesperado: %v", err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatalf("erro inesperado: %v", err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("lendo golden file: %v", err)
	}
	if string(data) != string(want) {
		t.Fatalf("golden file %s divergiu:\n%s", name, string(data))
	}
}

func TestProcessedGolden(t *testing.T) {
	env, err := NewTransactionProcessed(
		mustUUID(t, "0192f298-345e-7e38-af88-e43f851a819d"),
		mustUUID(t, "0192f298-345e-7e38-af88-e43f851a819d"),
		"corr-1", "",
		fixtureTime(),
		ProcessedData{
			TransactionID:       mustUUID(t, "0192f298-345e-7e38-af88-e43f851a819d"),
			ProviderID:          "provider-a",
			ExternalTxID:        "transaction-123",
			WalletID:            mustUUID(t, "0192f291-27dd-7d3f-8071-5f8685deef37"),
			PlayerID:            mustUUID(t, "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1"),
			RoundID:             "round-987",
			GameID:              "fortune-chimp",
			Kind:                "BET",
			Amount:              mustMoney(t, "25.00"),
			ResultBalance:       mustMoney(t, "975.00"),
			ResultWalletVersion: 2,
		},
	)
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	checkGolden(t, "processed", env)
}

func TestRejectedGolden(t *testing.T) {
	env, err := NewTransactionRejected(
		mustUUID(t, "0192f299-345e-7e38-af88-e43f851a819e"),
		mustUUID(t, "0192f299-345e-7e38-af88-e43f851a819e"),
		"corr-2", "cause-1",
		fixtureTime(),
		RejectedData{
			ProcessedData: ProcessedData{
				TransactionID:       mustUUID(t, "0192f299-345e-7e38-af88-e43f851a819e"),
				ProviderID:          "provider-a",
				ExternalTxID:        "transaction-124",
				WalletID:            mustUUID(t, "0192f291-27dd-7d3f-8071-5f8685deef37"),
				PlayerID:            mustUUID(t, "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1"),
				RoundID:             "round-987",
				GameID:              "fortune-chimp",
				Kind:                "BET",
				Amount:              mustMoney(t, "80.00"),
				ResultBalance:       mustMoney(t, "20.00"),
				ResultWalletVersion: 2,
			},
			FailureCode:   "INSUFFICIENT_FUNDS",
			FailureDetail: "saldo 20.00 menor que aposta 80.00",
		},
	)
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	checkGolden(t, "rejected", env)
}

func TestBalanceChangedGolden(t *testing.T) {
	env, err := NewBalanceChanged(
		mustUUID(t, "0192f29a-345e-7e38-af88-e43f851a819f"),
		mustUUID(t, "0192f291-27dd-7d3f-8071-5f8685deef37"),
		"corr-1", "",
		fixtureTime(),
		BalanceChangedData{
			WalletID:      mustUUID(t, "0192f291-27dd-7d3f-8071-5f8685deef37"),
			TransactionID: mustUUID(t, "0192f298-345e-7e38-af88-e43f851a819d"),
			Direction:     DirectionDebit,
			Amount:        mustMoney(t, "25.00"),
			BalanceBefore: mustMoney(t, "1000.00"),
			BalanceAfter:  mustMoney(t, "975.00"),
			WalletVersion: 2,
		},
	)
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	checkGolden(t, "balance_changed", env)
}

func TestPendingReferenceGolden(t *testing.T) {
	env, err := NewTransactionPendingReference(
		mustUUID(t, "0192f29b-345e-7e38-af88-e43f851a81a0"),
		mustUUID(t, "0192f29b-345e-7e38-af88-e43f851a81a0"),
		"corr-3",
		fixtureTime(),
		PendingReferenceData{
			TransactionID:  mustUUID(t, "0192f29b-345e-7e38-af88-e43f851a81a0"),
			ProviderID:     "provider-a",
			ExternalTxID:   "refund-1",
			WalletID:       mustUUID(t, "0192f291-27dd-7d3f-8071-5f8685deef37"),
			ReferenceExtID: "transaction-123",
			Attempts:       1,
			NextAttemptAt:  fixtureTime().Add(time.Second),
			ExpiresAt:      fixtureTime().Add(10 * time.Minute),
		},
	)
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	checkGolden(t, "pending_reference", env)
}

func TestOpeningCarriesNoExternalMetadata(t *testing.T) {
	// A abertura interna passa pelos mesmos construtores sem os metadados externos: eles somem do JSON em vez de saírem vazios.
	env, err := NewTransactionProcessed(
		mustUUID(t, "0192f29c-345e-7e38-af88-e43f851a81a1"),
		mustUUID(t, "0192f29c-345e-7e38-af88-e43f851a81a1"),
		"corr-4", "",
		fixtureTime(),
		ProcessedData{
			TransactionID:       mustUUID(t, "0192f29c-345e-7e38-af88-e43f851a81a1"),
			WalletID:            mustUUID(t, "0192f291-27dd-7d3f-8071-5f8685deef37"),
			PlayerID:            mustUUID(t, "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1"),
			Kind:                "OPENING",
			Amount:              mustMoney(t, "1000.00"),
			ResultBalance:       mustMoney(t, "1000.00"),
			ResultWalletVersion: 1,
		},
	)
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	data, _ := json.Marshal(env)
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	inner := decoded["data"].(map[string]any)
	for _, key := range []string{"providerId", "externalTransactionId", "roundId", "gameId"} {
		if _, ok := inner[key]; ok {
			t.Fatalf("metadado externo %q presente na abertura", key)
		}
	}
}
