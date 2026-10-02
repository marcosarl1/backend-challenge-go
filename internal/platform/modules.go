package platform

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"

	"github.com/marcosarl1/backend-challenge-go/internal/application"
	"github.com/marcosarl1/backend-challenge-go/internal/infra/auth"
	"github.com/marcosarl1/backend-challenge-go/internal/infra/httpapi"
	"github.com/marcosarl1/backend-challenge-go/internal/infra/postgres"
	sqsinfra "github.com/marcosarl1/backend-challenge-go/internal/infra/sqs"
	"github.com/marcosarl1/backend-challenge-go/internal/platform/config"
	"github.com/marcosarl1/backend-challenge-go/internal/platform/observability"
	"github.com/marcosarl1/backend-challenge-go/internal/workers/outbox"
	"github.com/marcosarl1/backend-challenge-go/internal/workers/pending"
)

// Module junta os módulos da aplicação.
var Module = fx.Module("platform",
	config.Module,
	observability.Module,
	DBModule,
	AWSModule,
	AuthModule,
	RepositoryModule,
	ApplicationModule,
	HTTPAPIModule,
	WorkersModule,
	fx.Invoke(registerComponents),
)

// DBModule configura o pool e as verificações do PostgreSQL.
var DBModule = fx.Module("db",
	fx.Provide(newPool),
	fx.Provide(postgres.NewUnitOfWork),
	fx.Provide(fx.Annotate(postgres.NewPingChecker,
		fx.As(new(httpapi.HealthChecker)), fx.ResultTags(`group:"health"`))),
)

// AWSModule configura o cliente e as filas do SQS.
var AWSModule = fx.Module("aws",
	fx.Provide(newSQSClient),
	fx.Provide(newQueueURLs),
	fx.Provide(fx.Annotate(newSQSHealthChecker,
		fx.As(new(httpapi.HealthChecker)), fx.ResultTags(`group:"health"`))),
)

// AuthModule configura a validação dos tokens OIDC.
var AuthModule = fx.Module("auth",
	fx.Provide(newVerifier),
)

// RepositoryModule conecta o executor às portas da aplicação.
var RepositoryModule = fx.Module("repo",
	fx.Provide(postgres.NewRunner),
	fx.Provide(newApplicationUnitOfWork),
)

// ApplicationModule configura relógio e geração de identificadores.
var ApplicationModule = fx.Module("app",
	fx.Provide(newClock),
	fx.Provide(newIDGenerator),
)

// HTTPAPIModule configura os handlers e o servidor HTTP.
var HTTPAPIModule = fx.Module("httpapi",
	fx.Provide(newHTTPHandler),
	fx.Provide(newHTTPServer),
)

// WorkersModule configura o consumidor e os workers agendados.
var WorkersModule = fx.Module("workers",
	fx.Provide(newConsumer),
	fx.Provide(newOutboxPublisher),
	fx.Provide(newPendingWorker),
)

func newPool(lifecycle fx.Lifecycle, cfg config.Config) (*pgxpool.Pool, error) {
	pool, err := postgres.Connect(context.Background(), cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}
	lifecycle.Append(fx.Hook{OnStop: func(context.Context) error {
		pool.Close()
		return nil
	}})
	return pool, nil
}

func newApplicationUnitOfWork(runner postgres.Runner) application.UnitOfWork {
	return runner
}

func newSQSClient(cfg config.Config) (*sqsinfra.Client, error) {
	return sqsinfra.NewClient(context.Background(), cfg.SQSEndpoint, cfg.SQSRegion)
}

type queueURLs struct {
	transactions string
	dlq          string
	events       string
}

func newQueueURLs(client *sqsinfra.Client) (queueURLs, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	transactions, err := client.ResolveQueue(ctx, sqsinfra.MainQueue)
	if err != nil {
		return queueURLs{}, err
	}
	dlq, err := client.ResolveQueue(ctx, sqsinfra.DLQQueue)
	if err != nil {
		return queueURLs{}, err
	}
	events, err := client.ResolveQueue(ctx, sqsinfra.EventsQueue)
	if err != nil {
		return queueURLs{}, err
	}
	return queueURLs{transactions: transactions, dlq: dlq, events: events}, nil
}

func newSQSHealthChecker(client *sqsinfra.Client) httpapi.HealthChecker {
	return client
}

func newVerifier(cfg config.Config) (*auth.Verifier, error) {
	return auth.NewVerifier(context.Background(), cfg.OIDCIssuer, cfg.OIDCJWKSURL, cfg.OIDCAudience)
}

func newClock() application.Clock { return application.SystemClock{} }

func newIDGenerator() application.IDGenerator { return application.UUIDv7Generator{} }

type httpParams struct {
	fx.In
	UOW    application.UnitOfWork
	Auth   *auth.Verifier
	Clock  application.Clock
	IDs    application.IDGenerator
	Checks []httpapi.HealthChecker `group:"health"`
}

func newHTTPHandler(p httpParams) http.Handler {
	return httpapi.New(p.UOW, p.Auth, p.Clock, p.IDs, p.Checks...).Handler()
}

func newHTTPServer(handler http.Handler, cfg config.Config) *http.Server {
	return httpapi.NewHTTPServer(handler, cfg.HTTPAddr)
}

func newConsumer(client *sqsinfra.Client, urls queueURLs, cfg config.Config, uow application.UnitOfWork, clock application.Clock, ids application.IDGenerator) *sqsinfra.Consumer {
	return sqsinfra.NewConsumer(client, urls.transactions, urls.dlq, cfg.ConsumerName, uow, clock, ids, cfg.ConsumerWorkers, int(cfg.ConsumerPoll.Seconds()))
}

func newOutboxPublisher(client *sqsinfra.Client, urls queueURLs, cfg config.Config, uow application.UnitOfWork, clock application.Clock) *outbox.Publisher {
	return outbox.NewPublisher(outbox.ClientSender{Client: client}, urls.events, uow, clock, cfg.OutboxOwner)
}

func newPendingWorker(cfg config.Config, uow application.UnitOfWork, clock application.Clock, ids application.IDGenerator) *pending.Worker {
	return pending.NewWorker(uow, clock, ids, cfg.RetryBatch, cfg.RetryInterval)
}

func registerComponents(server *http.Server, consumer *sqsinfra.Consumer, publisher *outbox.Publisher, retry *pending.Worker) error {
	if server == nil || server.Handler == nil || consumer == nil || publisher == nil || retry == nil {
		return fmt.Errorf("registrando componentes: dependência ausente")
	}
	return nil
}

// Validate verifica se o grafo de dependências está completo.
func Validate() error {
	if err := fx.ValidateApp(Module); err != nil {
		return fmt.Errorf("validando composição Fx: %w", err)
	}
	return nil
}
