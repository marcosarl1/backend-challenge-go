package sqs

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel/trace"

	"github.com/marcosarl1/backend-challenge-go/internal/application"
	"github.com/marcosarl1/backend-challenge-go/internal/platform/observability"
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
	logger       *slog.Logger
	metrics      *observability.Metrics
	tracing      *observability.Tracing
	workers      int
	pollSeconds  int
}

// NewConsumer monta o consumidor. workers limita quantas mensagens andam juntas; pollSeconds é a espera longa de cada puxada.
func NewConsumer(queues QueueOps, queueURL, dlqURL, consumerName string, uow application.UnitOfWork, clock application.Clock, ids application.IDGenerator, workers, pollSeconds int) *Consumer {
	return NewConsumerWithLogger(queues, queueURL, dlqURL, consumerName, uow, clock, ids, workers, pollSeconds, slog.Default())
}

// NewConsumerWithLogger monta o consumidor com logger estruturado.
func NewConsumerWithLogger(queues QueueOps, queueURL, dlqURL, consumerName string, uow application.UnitOfWork, clock application.Clock, ids application.IDGenerator, workers, pollSeconds int, logger *slog.Logger) *Consumer {
	return NewConsumerWithMetrics(queues, queueURL, dlqURL, consumerName, uow, clock, ids, workers, pollSeconds, logger, nil)
}

// NewConsumerWithMetrics monta o consumidor com logs e métricas.
func NewConsumerWithMetrics(queues QueueOps, queueURL, dlqURL, consumerName string, uow application.UnitOfWork, clock application.Clock, ids application.IDGenerator, workers, pollSeconds int, logger *slog.Logger, metrics *observability.Metrics) *Consumer {
	return NewConsumerWithTracing(queues, queueURL, dlqURL, consumerName, uow, clock, ids, workers, pollSeconds, logger, metrics, nil)
}

// NewConsumerWithTracing monta o consumidor com logs, métricas e spans.
func NewConsumerWithTracing(queues QueueOps, queueURL, dlqURL, consumerName string, uow application.UnitOfWork, clock application.Clock, ids application.IDGenerator, workers, pollSeconds int, logger *slog.Logger, metrics *observability.Metrics, tracing *observability.Tracing) *Consumer {
	if workers < 1 {
		workers = 1
	}
	if pollSeconds < 1 {
		pollSeconds = 1
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Consumer{queues: queues, queueURL: queueURL, dlqURL: dlqURL,
		consumerName: consumerName, uow: uow, clock: clock, ids: ids,
		workers: workers, pollSeconds: pollSeconds, logger: logger, metrics: metrics, tracing: tracing}
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
	if c.tracing != nil {
		ctx = observability.ExtractParent(ctx, msg.Traceparent)
		var span trace.Span
		ctx, span = c.tracing.Start(ctx, "sqs.consume", trace.SpanKindConsumer)
		defer span.End()
	}
	started := time.Now()
	if c.metrics != nil {
		defer func() { c.metrics.Latency("sqs_process", time.Since(started)) }()
	}
	ctx = observability.WithFields(ctx, slog.String("messageId", msg.MessageID))
	cmd, hash, err := parseEnvelope([]byte(msg.Body))
	if err != nil {
		c.toDLQ(ctx, msg, "envelope inválido: "+err.Error())
		return
	}
	ctx = observability.WithFields(ctx,
		slog.String("messageId", cmd.MessageID),
		slog.String("correlationId", cmd.Command.CorrelationID),
		slog.String("providerId", cmd.Command.ProviderID),
		slog.String("walletId", cmd.Command.WalletID.String()),
	)
	var poison string
	var transactionID string
	var status string
	var duplicate bool
	err = c.uow.Do(ctx, func(ctx context.Context, r application.Repositories) error {
		poison, transactionID, status, duplicate = "", "", "", false
		inserted, err := r.Inbox.Insert(ctx, c.consumerName, cmd.MessageID, hash, c.clock.Now())
		if err != nil {
			return err
		}
		if !inserted {
			duplicate = true
			stored, err := r.Inbox.HashOf(ctx, c.consumerName, cmd.MessageID)
			if err != nil {
				return err
			}
			if !equalHash(stored, hash) {
				poison = "veneno: mesmo messageId com outro conteúdo: " + cmd.MessageID
			}
			return nil
		}
		result, err := application.ExecuteInTx(ctx, r, c.clock, c.ids, cmd.Command)
		if result != nil {
			transactionID = result.TransactionID.String()
			status = string(result.Status)
			duplicate = result.IdempotentReplay
		}
		return err
	})
	if transactionID != "" {
		ctx = observability.WithFields(ctx, slog.String("transactionId", transactionID))
	}
	if err != nil {
		if isPermanent(err) {
			c.toDLQ(ctx, msg, "permanente: "+err.Error())
			return
		}
		// Transitório: solta com espera crescente pela contagem de recebimentos; esgotada, o redrive leva para a DLQ sozinho.
		c.logger.ErrorContext(ctx, "consumidor: soltando para retry", "error", err)
		if rerr := c.release(ctx, msg, backoffDelay(msg.ReceiveCount)); rerr != nil {
			c.logger.ErrorContext(ctx, "consumidor: soltura falhou", "error", rerr)
		} else if c.metrics != nil {
			c.metrics.Retry("sqs")
		}
		return
	}
	if c.metrics != nil {
		if status != "" {
			c.metrics.Result("sqs", status)
		}
		if duplicate && poison == "" {
			c.metrics.Duplicate("sqs")
		}
	}
	if poison != "" {
		c.toDLQ(ctx, msg, poison)
		return
	}
	if derr := c.queues.Delete(ctx, c.queueURL, msg.ReceiptHandle); derr != nil {
		c.logger.ErrorContext(ctx, "consumidor: delete falhou", "error", derr)
		if ctx.Err() != nil {
			if rerr := c.release(ctx, msg, 0); rerr != nil {
				c.logger.ErrorContext(ctx, "consumidor: soltura após cancelamento falhou", "error", rerr)
			}
		}
		return
	}
	c.logger.InfoContext(ctx, "mensagem confirmada")
}

// toDLQ copia para a DLQ com o motivo e tira a original da fila.
func (c *Consumer) toDLQ(ctx context.Context, msg Received, reason string) {
	if serr := c.queues.SendDLQ(ctx, c.dlqURL, msg.Body, reason, msg.MessageID); serr != nil {
		c.logger.ErrorContext(ctx, "consumidor: envio à DLQ falhou", "error", serr)
		if ctx.Err() != nil {
			if rerr := c.release(ctx, msg, 0); rerr != nil {
				c.logger.ErrorContext(ctx, "consumidor: soltura após cancelamento falhou", "error", rerr)
			}
		}
		return
	}
	if derr := c.queues.Delete(ctx, c.queueURL, msg.ReceiptHandle); derr != nil {
		c.logger.ErrorContext(ctx, "consumidor: delete após DLQ falhou", "error", derr)
		if ctx.Err() != nil {
			if rerr := c.release(ctx, msg, 0); rerr != nil {
				c.logger.ErrorContext(ctx, "consumidor: soltura após cancelamento falhou", "error", rerr)
			}
		}
		return
	}
	c.logger.InfoContext(ctx, "mensagem enviada à DLQ")
	if c.metrics != nil {
		c.metrics.DLQ()
	}
}

// release libera a mensagem e usa um prazo independente se o trabalho foi cancelado.
func (c *Consumer) release(ctx context.Context, msg Received, delay int) error {
	releaseCtx := ctx
	if ctx.Err() != nil {
		var cancel context.CancelFunc
		releaseCtx, cancel = context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
	}
	return c.queues.Release(releaseCtx, c.queueURL, msg.ReceiptHandle, delay)
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
