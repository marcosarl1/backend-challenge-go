package sqs

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/marcosarl1/backend-challenge-go/internal/application"
)

// QueueOps é o que o consumidor precisa da fila. O cliente real implementa; o teste de recuperação injeta um dublê que falha sob comando.
type QueueOps interface {
	Receive(ctx context.Context, queueURL string, max, waitSeconds int) ([]Received, error)
	Delete(ctx context.Context, queueURL, receiptHandle string) error
	Release(ctx context.Context, queueURL, receiptHandle string, delaySeconds int) error
	SendDLQ(ctx context.Context, dlqURL, originalBody, reason, dedupID string) error
}

// Consumer puxa operações da fila com espera longa e aplica cada uma na mesma transação do banco que registra a inbox e os eventos. A mensagem só sai da fila depois do commit; rejeição de negócio é fim definitivo (sai também); transitório solta com espera crescente e tenta de novo.
type Consumer struct {
	queues       QueueOps
	queueURL     string
	dlqURL       string
	consumerName string
	uow          application.UnitOfWork
	clock        application.Clock
	ids          application.IDGenerator
	workers      int
	pollSeconds  int
}

// NewConsumer monta o consumidor. workers limita quantas mensagens andam juntas; pollSeconds é a espera longa de cada puxada.
func NewConsumer(queues QueueOps, queueURL, dlqURL, consumerName string, uow application.UnitOfWork, clock application.Clock, ids application.IDGenerator, workers, pollSeconds int) *Consumer {
	if workers < 1 {
		workers = 1
	}
	if pollSeconds < 1 {
		pollSeconds = 1
	}
	return &Consumer{queues: queues, queueURL: queueURL, dlqURL: dlqURL,
		consumerName: consumerName, uow: uow, clock: clock, ids: ids,
		workers: workers, pollSeconds: pollSeconds}
}

// Run puxa e aplica até o contexto acabar. Para de buscar ao cancelar e termina o lote em voo antes de voltar.
func (c *Consumer) Run(ctx context.Context) error {
	sem := make(chan struct{}, c.workers)
	for {
		if ctx.Err() != nil {
			return nil
		}
		messages, err := c.queues.Receive(ctx, c.queueURL, 10, c.pollSeconds)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("sqs: puxando: %w", err)
		}
		done := make(chan struct{}, len(messages))
		for _, msg := range messages {
			sem <- struct{}{}
			go func(msg Received) {
				defer func() { <-sem; done <- struct{}{} }()
				c.handle(ctx, msg)
			}(msg)
		}
		for range messages {
			<-done
		}
	}
}

// handle trata uma mensagem: inválida vai para a DLQ com motivo; repetida com o mesmo conteúdo confirma sem reexecutar; com outro conteúdo é veneno e vai para a DLQ; o resto segue para o caso de uso na transação.

func (c *Consumer) handle(ctx context.Context, msg Received) {
	cmd, hash, err := parseEnvelope([]byte(msg.Body))
	if err != nil {
		c.toDLQ(ctx, msg, "envelope inválido: "+err.Error())
		return
	}
	var poison string
	err = c.uow.Do(ctx, func(ctx context.Context, r application.Repositories) error {
		inserted, err := r.Inbox.Insert(ctx, c.consumerName, cmd.MessageID, hash, c.clock.Now())
		if err != nil {
			return err
		}
		if !inserted {
			stored, err := r.Inbox.HashOf(ctx, c.consumerName, cmd.MessageID)
			if err != nil {
				return err
			}
			if !equalHash(stored, hash) {
				poison = "veneno: mesmo messageId com outro conteúdo: " + cmd.MessageID
			}
			return nil
		}
		_, err = application.ExecuteInTx(ctx, r, c.clock, c.ids, cmd.Command)
		return err
	})
	if err != nil {
		if isPermanent(err) {
			c.toDLQ(ctx, msg, "permanente: "+err.Error())
			return
		}
		// Transitório: solta com espera crescente pela contagem de recebimentos; esgotada, o redrive leva para a DLQ sozinho.
		slog.ErrorContext(ctx, "consumidor: soltando para retry",
			"messageId", msg.MessageID, "body", firstBytes(msg.Body, 120), "error", fmt.Sprintf("%#v", err))
		if rerr := c.queues.Release(ctx, c.queueURL, msg.ReceiptHandle, backoffDelay(msg.ReceiveCount)); rerr != nil {
			slog.ErrorContext(ctx, "consumidor: soltura falhou", "messageId", msg.MessageID, "error", rerr)
		}
		return
	}
	if poison != "" {
		c.toDLQ(ctx, msg, poison)
		return
	}
	if derr := c.queues.Delete(ctx, c.queueURL, msg.ReceiptHandle); derr != nil {
		slog.ErrorContext(ctx, "consumidor: delete falhou", "messageId", msg.MessageID, "error", derr)
	}
}

// toDLQ copia para a DLQ com o motivo e tira a original da fila.
func (c *Consumer) toDLQ(ctx context.Context, msg Received, reason string) {
	if serr := c.queues.SendDLQ(ctx, c.dlqURL, msg.Body, reason, msg.MessageID); serr != nil {
		slog.ErrorContext(ctx, "consumidor: envio à DLQ falhou", "messageId", msg.MessageID, "error", serr)
		return
	}
	if derr := c.queues.Delete(ctx, c.queueURL, msg.ReceiptHandle); derr != nil {
		slog.ErrorContext(ctx, "consumidor: delete após DLQ falhou", "messageId", msg.MessageID, "error", derr)
	}
}

// backoffDelay espera 2^(n-1) segundos até o teto de um minuto.
func backoffDelay(receiveCount int) int {
	if receiveCount < 1 {
		receiveCount = 1
	}
	delay := 1 << min(receiveCount-1, 6)
	if delay > 60 {
		return 60
	}
	return delay
}

// firstBytes corta o corpo para o log (sem despejar payload financeiro).
func firstBytes(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

func equalHash(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	match := true
	for i := range a {
		if a[i] != b[i] {
			match = false
		}
	}
	return match
}
