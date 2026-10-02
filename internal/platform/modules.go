package platform

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
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
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validando configuração do pool: %w", err)
	}
	pool, err := postgres.Connect(context.Background(), cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}
	lifecycle.Append(fx.Hook{
		OnStart: func(context.Context) error { return nil },
		OnStop: func(context.Context) error {
			pool.Close()
			return nil
		},
	})
	return pool, nil
}

func newApplicationUnitOfWork(runner postgres.Runner) application.UnitOfWork {
	return runner
}

func newSQSClient(cfg config.Config) (*sqsinfra.Client, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validando configuração do SQS: %w", err)
	}
	return sqsinfra.NewClient(context.Background(), cfg.SQSEndpoint, cfg.SQSRegion)
}

type queueURLs struct {
	transactions string
	dlq          string
	events       string
}

func newQueueURLs(client *sqsinfra.Client, cfg config.Config) (queueURLs, error) {
	if err := cfg.Validate(); err != nil {
		return queueURLs{}, fmt.Errorf("validando configuração das filas: %w", err)
	}
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
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validando configuração OIDC: %w", err)
	}
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

// runtimeParams reúne as dependências do ciclo de vida da aplicação.
type runtimeParams struct {
	fx.In
	Lifecycle fx.Lifecycle
	Shutdown  fx.Shutdowner
	Server    *http.Server
	Checks    []httpapi.HealthChecker `group:"health"`
	Consumer  *sqsinfra.Consumer
	Publisher *outbox.Publisher
	Retry     *pending.Worker
	Config    config.Config
}

// registerComponents conecta os serviços gerenciados ao lifecycle do Fx.
func registerComponents(p runtimeParams) error {
	server, consumer, publisher, retry := p.Server, p.Consumer, p.Publisher, p.Retry
	if server == nil || server.Handler == nil || consumer == nil || publisher == nil || retry == nil {
		return fmt.Errorf("registrando componentes: dependência ausente")
	}
	_, err := registerLifecycle(p.Lifecycle, p.Shutdown, server, p.Checks, p.Config,
		componentRunner{name: "consumidor SQS", run: consumer.Run},
		componentRunner{name: "publicador da outbox", run: func(ctx context.Context) error {
			return publisher.Run(ctx, p.Config.OutboxInterval)
		}},
		componentRunner{name: "retomada de pendências", run: retry.Run},
	)
	return err
}

// componentRunner representa um loop iniciado e cancelado pelo runtime.
type componentRunner struct {
	name string
	run  func(context.Context) error
}

// registerLifecycle instala os hooks que iniciam e encerram o servidor e os workers.
func registerLifecycle(lifecycle fx.Lifecycle, shutdown fx.Shutdowner, server *http.Server, checks []httpapi.HealthChecker, cfg config.Config, runners ...componentRunner) (*managedRuntime, error) {
	if server == nil || server.Handler == nil || shutdown == nil {
		return nil, fmt.Errorf("registrando lifecycle: dependência ausente")
	}
	managed := &managedRuntime{server: server, checks: checks, config: cfg, shutdown: shutdown, runners: runners}
	lifecycle.Append(fx.Hook{OnStart: managed.start, OnStop: managed.stop})
	return managed, nil
}

// managedRuntime coordena os componentes durante o start e o shutdown.
type managedRuntime struct {
	server   *http.Server
	checks   []httpapi.HealthChecker
	config   config.Config
	shutdown fx.Shutdowner
	runners  []componentRunner
	cancel   context.CancelFunc
	listener net.Listener
	workers  sync.WaitGroup
}

// start valida dependências, abre o listener e inicia os componentes gerenciados.
func (r *managedRuntime) start(ctx context.Context) error {
	if err := r.config.Validate(); err != nil {
		return fmt.Errorf("validando configuração: %w", err)
	}
	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for _, check := range r.checks {
		if err := check.Check(checkCtx); err != nil {
			return fmt.Errorf("dependência %s indisponível: %w", check.Name(), err)
		}
	}
	listener, err := net.Listen("tcp", r.server.Addr)
	if err != nil {
		return fmt.Errorf("abrindo listener HTTP: %w", err)
	}
	r.listener = listener
	runCtx, stop := context.WithCancel(context.Background())
	r.cancel = stop
	r.launch(runCtx, componentRunner{name: "servidor HTTP", run: func(context.Context) error {
		err := r.server.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}})
	for _, runner := range r.runners {
		r.launch(runCtx, runner)
	}
	return nil
}

// launch executa um componente e solicita o encerramento se ele falhar.
func (r *managedRuntime) launch(ctx context.Context, runner componentRunner) {
	r.workers.Add(1)
	go func() {
		defer r.workers.Done()
		if err := runner.run(ctx); err != nil && ctx.Err() == nil {
			slog.ErrorContext(ctx, "componente encerrou com falha", "component", runner.name, "error", err)
			if shutdownErr := r.shutdown.Shutdown(fx.ExitCode(1)); shutdownErr != nil {
				slog.ErrorContext(ctx, "solicitação de encerramento falhou", "error", shutdownErr)
			}
		}
	}()
}

// stop interrompe entradas, cancela os workers e aguarda seu término.
func (r *managedRuntime) stop(ctx context.Context) error {
	var stopErr error
	if r.server != nil {
		stopErr = r.server.Shutdown(ctx)
		if stopErr != nil {
			stopErr = errors.Join(stopErr, r.server.Close())
		}
	}
	if r.cancel != nil {
		r.cancel()
	}
	done := make(chan struct{})
	go func() {
		r.workers.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		stopErr = errors.Join(stopErr, ctx.Err())
	}
	return stopErr
}

// Validate verifica se o grafo de dependências está completo.
func Validate() error {
	if err := fx.ValidateApp(Module); err != nil {
		return fmt.Errorf("validando composição Fx: %w", err)
	}
	return nil
}
