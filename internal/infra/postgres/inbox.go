package postgres

import (
	"context"
	"fmt"
	"time"
)

// InboxStore registra as mensagens recebidas da fila na mesma transação que as aplica: mensagem repetida com o mesmo conteúdo é confirmada sem reexecutar; com conteúdo diferente, é veneno e vai para a DLQ.
type InboxStore struct{}

// Insert tenta registrar (consumidor, mensagem). Devolve false se já existe.
func (InboxStore) Insert(ctx context.Context, db DBTX, consumer, messageID string, hash []byte, now time.Time) (bool, error) {
	tag, err := db.Exec(ctx, `INSERT INTO inbox_messages (consumer_name, message_id, payload_hash, received_at, completed_at)
		VALUES ($1, $2, $3, $4, $4) ON CONFLICT (consumer_name, message_id) DO NOTHING`,
		consumer, messageID, hash, now)
	if err != nil {
		return false, fmt.Errorf("registrando inbox: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// HashOf devolve o hash guardado para comparar com a reentrega.
func (InboxStore) HashOf(ctx context.Context, db DBTX, consumer, messageID string) ([]byte, error) {
	var hash []byte
	if err := db.QueryRow(ctx, `SELECT payload_hash FROM inbox_messages
		WHERE consumer_name = $1 AND message_id = $2`, consumer, messageID).Scan(&hash); err != nil {
		return nil, fmt.Errorf("lendo inbox: %w", err)
	}
	return hash, nil
}
