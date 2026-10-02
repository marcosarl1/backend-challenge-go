package outbox

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/google/uuid"

	"github.com/marcosarl1/backend-challenge-go/internal/application"
	"github.com/marcosarl1/backend-challenge-go/internal/infra/sqs"
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

	AfterPublish func(id uuid.UUID) error
}

// NewPublisher monta o publicador dono de um nome único (hostname+pid, por exemplo) para disputar os arrendamentos.
func NewPublisher(queues Sender, eventsURL string, uow application.UnitOfWork, clock application.Clock, owner string) *Publisher {
	return &Publisher{queues: queues, eventsURL: eventsURL, uow: uow, clock: clock,
		owner: owner, batchSize: 10, leaseFor: 30 * time.Second,
		baseDelay: time.Second, maxDelay: time.Minute}
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
				slog.ErrorContext(ctx, "publicador: lote falhou", "error", err)
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
		if err := p.publishOne(ctx, event); err != nil {
			slog.ErrorContext(ctx, "publicador: evento falhou",
				"eventId", event.ID.String(), "error", err)
			continue
		}
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
