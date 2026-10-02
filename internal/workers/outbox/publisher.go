package outbox

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/marcosarl1/backend-challenge-go/internal/application"
	"github.com/marcosarl1/backend-challenge-go/internal/infra/sqs"
	"github.com/marcosarl1/backend-challenge-go/internal/platform/observability"
)

// ClientSender adapta o cliente SQS para a porta do publicador.
type ClientSender struct {
	Client *sqs.Client
}

// Send publica ignorando o id de mensagem (o eventId já identifica).
func (s ClientSender) Send(ctx context.Context, queueURL, body, groupID, dedupID string) error {
	_, err := s.Client.Send(ctx, queueURL, body, groupID, dedupID)
	return err
}

// Sender publica um evento na fila de saída.
type Sender interface {
	Send(ctx context.Context, queueURL, body, groupID, dedupID string) error
}

// Publisher tira eventos pendentes do banco e publica, com arrendamento para disputar entre instâncias: reserva um lote, publica fora da transação, confirma ou remarca. Republicação mantém o mesmo eventId (quem consome deduplica por ele). AfterPublish é gancho de teste para simular queda entre publicar e confirmar.
type Publisher struct {
	queues    Sender
	eventsURL string
	uow       application.UnitOfWork
	clock     application.Clock
	owner     string
	batchSize int
	leaseFor  time.Duration
	baseDelay time.Duration
	maxDelay  time.Duration
	logger    *slog.Logger
	metrics   *observability.Metrics
	tracing   *observability.Tracing

	AfterPublish func(id uuid.UUID) error
}

// NewPublisher monta o publicador dono de um nome único (hostname+pid, por exemplo) para disputar os arrendamentos.
func NewPublisher(queues Sender, eventsURL string, uow application.UnitOfWork, clock application.Clock, owner string) *Publisher {
	return NewPublisherWithLogger(queues, eventsURL, uow, clock, owner, slog.Default())
}

// NewPublisherWithLogger monta o publicador com logger estruturado.
func NewPublisherWithLogger(queues Sender, eventsURL string, uow application.UnitOfWork, clock application.Clock, owner string, logger *slog.Logger) *Publisher {
	return NewPublisherWithMetrics(queues, eventsURL, uow, clock, owner, logger, nil)
}

// NewPublisherWithMetrics monta o publicador com logs e métricas.
func NewPublisherWithMetrics(queues Sender, eventsURL string, uow application.UnitOfWork, clock application.Clock, owner string, logger *slog.Logger, metrics *observability.Metrics) *Publisher {
	return NewPublisherWithTracing(queues, eventsURL, uow, clock, owner, logger, metrics, nil)
}

// NewPublisherWithTracing monta o publicador com logs, métricas e spans.
func NewPublisherWithTracing(queues Sender, eventsURL string, uow application.UnitOfWork, clock application.Clock, owner string, logger *slog.Logger, metrics *observability.Metrics, tracing *observability.Tracing) *Publisher {
	if logger == nil {
		logger = slog.Default()
	}
	return &Publisher{queues: queues, eventsURL: eventsURL, uow: uow, clock: clock,
		owner: owner, batchSize: 10, leaseFor: 30 * time.Second,
		baseDelay: time.Second, maxDelay: time.Minute, logger: logger, metrics: metrics, tracing: tracing}
}

// Run publica lotes até o contexto acabar.
func (p *Publisher) Run(ctx context.Context, interval time.Duration) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if _, err := p.RunOnce(ctx); err != nil {
				p.logger.ErrorContext(ctx, "publicador: lote falhou", "error", err)
			}
		}
	}
}

// RunOnce reserva um lote e publica. Devolve quantos confirmou.
func (p *Publisher) RunOnce(ctx context.Context) (int, error) {
	now := p.clock.Now()
	var claimed []application.OutboxClaim
	err := p.uow.Do(ctx, func(ctx context.Context, r application.Repositories) error {
		due, err := r.Outbox.Claim(ctx, p.owner, now, now.Add(p.leaseFor), p.batchSize)
		if err != nil {
			return err
		}
		claimed = due
		return nil
	})
	if err != nil {
		return 0, err
	}
	published := 0
	for _, event := range claimed {
		started := time.Now()
		eventCtx := observability.ExtractParent(ctx, event.Traceparent)
		var span trace.Span
		if p.tracing != nil {
			eventCtx, span = p.tracing.Start(eventCtx, "outbox.publish", trace.SpanKindInternal)
		}
		eventCtx = observability.WithFields(eventCtx,
			slog.String("eventId", event.ID.String()),
			slog.String("correlationId", event.CorrelationID),
			slog.String("walletId", event.OrderingKey),
		)
		if event.AggregateType == application.AggregateWager {
			eventCtx = observability.WithFields(eventCtx, slog.String("transactionId", event.AggregateID.String()))
		}
		if err := p.publishOne(eventCtx, event); err != nil {
			if span != nil {
				span.SetStatus(codes.Error, "publish failed")
				span.End()
			}
			p.logger.ErrorContext(eventCtx, "publicador: evento falhou", "error", err)
			if p.metrics != nil {
				p.metrics.Latency("outbox_publish", time.Since(started))
			}
			continue
		}
		if p.metrics != nil {
			p.metrics.OutboxDelay(p.clock.Now().Sub(event.OccurredAt))
			p.metrics.Latency("outbox_publish", time.Since(started))
		}
		if span != nil {
			span.End()
		}
		p.logger.InfoContext(eventCtx, "evento publicado")
		published++
	}
	return published, nil
}

func (p *Publisher) publishOne(ctx context.Context, event application.OutboxClaim) error {
	now := p.clock.Now()
	if err := p.queues.Send(ctx, p.eventsURL, string(event.Payload), event.OrderingKey, event.ID.String()); err != nil {
		return p.deferFailed(ctx, event, now, err)
	}
	if p.AfterPublish != nil {
		if err := p.AfterPublish(event.ID); err != nil {
			return err
		}
	}
	return p.uow.Do(ctx, func(ctx context.Context, r application.Repositories) error {
		return r.Outbox.MarkPublished(ctx, event.ID, p.owner, p.clock.Now())
	})
}

func (p *Publisher) deferFailed(ctx context.Context, event application.OutboxClaim, now time.Time, cause error) error {
	delay := backoffWithJitter(p.baseDelay, p.maxDelay, event.Attempts)
	err := p.uow.Do(ctx, func(ctx context.Context, r application.Repositories) error {
		return r.Outbox.DeferFailed(ctx, event.ID, p.owner, now.Add(delay), cause.Error())
	})
	if err != nil {
		return err
	}
	if p.metrics != nil {
		p.metrics.Retry("outbox")
	}
	return cause
}

// backoffWithJitter espera base dobrando por tentativa até o teto, com
// sorteio de metade a metade (evita que vários publicadores acordem juntos).
func backoffWithJitter(base, max time.Duration, attempts int) time.Duration {
	delay := base << attempts
	if delay <= 0 || delay > max {
		delay = max
	}
	half := int64(delay) / 2
	return time.Duration(half + rand.N(half+1))
}
