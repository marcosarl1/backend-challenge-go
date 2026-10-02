package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/marcosarl1/backend-challenge-go/internal/platform/observability"
)

// OutboxEvent é o registro pendente de publicação: identidade estável (a republicação mantém o mesmo id), payload como retrato e controle de tentativa e arrendamento para disputar entre publicadores.
type OutboxEvent struct {
	ID            uuid.UUID
	AggregateType string
	AggregateID   uuid.UUID
	EventType     string
	EventVersion  int
	OrderingKey   string
	CorrelationID string
	Traceparent   string
	CausationID   string
	Payload       []byte
	OccurredAt    time.Time
	Attempts      int
}

// OutboxStore grava e reserva eventos para o publicador.
type OutboxStore struct{}

// Insert enfileira o evento na mesma transação da operação que o originou.
func (OutboxStore) Insert(ctx context.Context, db DBTX, e OutboxEvent, nextAttempt time.Time) error {
	var causation any
	if e.CausationID != "" {
		causation = e.CausationID
	}
	if e.Traceparent == "" {
		e.Traceparent = observability.InjectParent(ctx)
	}
	_, err := db.Exec(ctx, `INSERT INTO outbox_events
		(id, aggregate_type, aggregate_id, event_type, event_version, ordering_key,
		 correlation_id, causation_id, payload, occurred_at, next_attempt_at, traceparent)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		toPGUUID(e.ID), e.AggregateType, toPGUUID(e.AggregateID), e.EventType, e.EventVersion,
		e.OrderingKey, e.CorrelationID, causation, e.Payload, e.OccurredAt, nextAttempt, e.Traceparent)
	if err != nil {
		return fmt.Errorf("enfileirando evento: %w", err)
	}
	return nil
}

// Claim reserva um lote vencido e sem dono (ou com arrendamento expirado) para este publicador, pulando os que outro pegou. A reserva e a contagem de tentativa acontecem juntas, na transação curta de quem reserva.
func (OutboxStore) Claim(ctx context.Context, db DBTX, owner string, now, leaseUntil time.Time, limit int) ([]OutboxEvent, error) {
	rows, err := db.Query(ctx, `UPDATE outbox_events SET lease_owner = $1, lease_until = $2, attempts = attempts + 1
		WHERE id IN (SELECT id FROM outbox_events
			WHERE published_at IS NULL AND next_attempt_at <= $3
			  AND (lease_until IS NULL OR lease_until < $3)
			ORDER BY occurred_at LIMIT $4 FOR UPDATE SKIP LOCKED)
		RETURNING id, aggregate_type, aggregate_id, event_type, event_version,
			ordering_key, correlation_id, causation_id, payload, occurred_at, attempts, traceparent`,
		owner, leaseUntil, now, limit)
	if err != nil {
		return nil, fmt.Errorf("reservando eventos: %w", err)
	}
	defer rows.Close()
	var out []OutboxEvent
	for rows.Next() {
		var e OutboxEvent
		var id, agg pgtype.UUID
		var causation *string
		var traceparent *string
		if err := rows.Scan(&id, &e.AggregateType, &agg, &e.EventType, &e.EventVersion,
			&e.OrderingKey, &e.CorrelationID, &causation, &e.Payload, &e.OccurredAt, &e.Attempts, &traceparent); err != nil {
			return nil, fmt.Errorf("lendo evento: %w", err)
		}
		var err error
		if e.ID, err = fromPGUUID("id", id); err != nil {
			return nil, err
		}
		if e.AggregateID, err = fromPGUUID("aggregate_id", agg); err != nil {
			return nil, err
		}
		if causation != nil {
			e.CausationID = *causation
		}
		if traceparent != nil {
			e.Traceparent = *traceparent
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// MarkPublished confirma a publicação feita sob o arrendamento.
func (OutboxStore) MarkPublished(ctx context.Context, db DBTX, id uuid.UUID, owner string, now time.Time) error {
	tag, err := db.Exec(ctx, `UPDATE outbox_events SET published_at = $2
		WHERE id = $1 AND lease_owner = $3 AND published_at IS NULL`,
		toPGUUID(id), now, owner)
	if err != nil {
		return fmt.Errorf("confirmando evento: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("evento %s sem arrendamento de %s", id, owner)
	}
	return nil
}

// DeferFailed remarca com espera crescente e solta o arrendamento para outro publicador tentar depois do próximo vencimento.
func (OutboxStore) DeferFailed(ctx context.Context, db DBTX, id uuid.UUID, owner string, next time.Time, lastErr string) error {
	tag, err := db.Exec(ctx, `UPDATE outbox_events
		SET next_attempt_at = $2, last_error = $3, lease_owner = NULL, lease_until = NULL
		WHERE id = $1 AND lease_owner = $4 AND published_at IS NULL`,
		toPGUUID(id), next, lastErr, owner)
	if err != nil {
		return fmt.Errorf("remarcando evento: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("evento %s sem arrendamento de %s", id, owner)
	}
	return nil
}
