package platform

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/marcosarl1/backend-challenge-go/internal/infra/httpapi"
	"github.com/marcosarl1/backend-challenge-go/internal/platform/config"
	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"
	"go.uber.org/goleak"
)

type lifecycleHealthCheck struct {
	err error
}

func (c lifecycleHealthCheck) Name() string { return "test" }
func (c lifecycleHealthCheck) Check(context.Context) error {
	return c.err
}

func TestLifecycleStartsAndStopsComponents(t *testing.T) {
	defer goleak.VerifyNone(t)
	stopped := make(chan struct{})
	runtime, app := newLifecycleTestApp(t, validRuntimeConfig(), nil, componentRunner{
		name: "worker",
		run: func(ctx context.Context) error {
			<-ctx.Done()
			close(stopped)
			return nil
		},
	})
	app.RequireStart()

	response, err := http.Get("http://" + runtime.listener.Addr().String())
	if err != nil {
		t.Fatalf("requisição ao servidor iniciado: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, esperado %d", response.StatusCode, http.StatusNoContent)
	}

	app.RequireStop()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("worker não terminou após o shutdown")
	}
}

// TestLifecycleStopsAllWorkers confirma que o Fx espera todos os loops após cancelar o contexto.
func TestLifecycleStopsAllWorkers(t *testing.T) {
	defer goleak.VerifyNone(t)
	started := make(chan string, 3)
	stopped := make(chan string, 3)
	runners := make([]componentRunner, 0, 3)
	for _, name := range []string{"consumidor SQS", "publicador da outbox", "retomada de pendências"} {
		runners = append(runners, componentRunner{name: name, run: func(ctx context.Context) error {
			started <- name
			<-ctx.Done()
			stopped <- name
			return nil
		}})
	}
	cfg := validRuntimeConfig()
	app := fxtest.New(t, fx.NopLogger, fx.Provide(func() config.Config { return cfg }),
		fx.Provide(func() *http.Server {
			return httpapi.NewHTTPServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			}), cfg.HTTPAddr)
		}),
		fx.Invoke(func(lifecycle fx.Lifecycle, shutdown fx.Shutdowner, server *http.Server, cfg config.Config) error {
			_, err := registerLifecycle(lifecycle, shutdown, server, nil, cfg, runners...)
			return err
		}))
	app.RequireStart()
	for range runners {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("worker não iniciou")
		}
	}
	app.RequireStop()
	for range runners {
		select {
		case <-stopped:
		case <-time.After(time.Second):
			t.Fatal("worker não terminou")
		}
	}
}

func TestLifecycleRejectsInvalidConfigBeforeStarting(t *testing.T) {
	cfg := validRuntimeConfig()
	cfg.ConsumerWorkers = 0
	_, app := newLifecycleTestApp(t, cfg, nil, componentRunner{name: "worker", run: func(context.Context) error {
		t.Fatal("worker não deveria iniciar")
		return nil
	}})
	if err := app.Start(context.Background()); err == nil || !strings.Contains(err.Error(), "consumidor SQS") {
		t.Fatalf("Start() = %v, esperado erro de configuração", err)
	}
	app.RequireStop()
}

func TestLifecycleChecksDependenciesBeforeListening(t *testing.T) {
	_, app := newLifecycleTestApp(t, validRuntimeConfig(), []httpapi.HealthChecker{
		lifecycleHealthCheck{err: fmt.Errorf("indisponível")},
	}, componentRunner{name: "worker", run: func(context.Context) error { return nil }})
	if err := app.Start(context.Background()); err == nil || !strings.Contains(err.Error(), "dependência test indisponível") {
		t.Fatalf("Start() = %v, esperado erro de prontidão", err)
	}
	app.RequireStop()
}

func TestSIGTERMProcess(t *testing.T) {
	if os.Getenv("FX_SIGTERM_HELPER") == "1" {
		cfg := validRuntimeConfig()
		cfg.HTTPAddr = "127.0.0.1:0"
		app := fx.New(lifecycleTestOptions(cfg, nil, componentRunner{
			name: "worker", run: func(ctx context.Context) error { <-ctx.Done(); return nil },
		}, nil)...)
		if err := app.Err(); err != nil {
			t.Fatal(err)
		}
		if err := app.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		done := app.Wait()
		_, _ = fmt.Fprintln(os.Stdout, "LIFECYCLE_READY")
		<-done
		if err := app.Stop(context.Background()); err != nil {
			t.Fatal(err)
		}
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestSIGTERMProcess$")
	cmd.Env = append(os.Environ(), "FX_SIGTERM_HELPER=1")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	ready := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if scanner.Text() == "LIFECYCLE_READY" {
				ready <- nil
				return
			}
		}
		ready <- fmt.Errorf("processo terminou antes de iniciar: %v", scanner.Err())
	}()
	select {
	case err := <-ready:
		if err != nil {
			_ = cmd.Process.Kill()
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("processo não sinalizou que iniciou")
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		_ = cmd.Process.Kill()
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("processo terminou com falha após SIGTERM: %v", err)
		}
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("processo não encerrou dentro do prazo")
	}
}

func newLifecycleTestApp(t *testing.T, cfg config.Config, checks []httpapi.HealthChecker, runner componentRunner) (*managedRuntime, *fxtest.App) {
	t.Helper()
	var managed *managedRuntime
	app := fxtest.New(t, lifecycleTestOptions(cfg, checks, runner, &managed)...)
	return managed, app
}

func lifecycleTestOptions(cfg config.Config, checks []httpapi.HealthChecker, runner componentRunner, managedOut **managedRuntime, extra ...fx.Option) []fx.Option {
	options := []fx.Option{
		fx.NopLogger,
		fx.Provide(func() config.Config { return cfg }),
		fx.Provide(func() *http.Server {
			return httpapi.NewHTTPServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			}), cfg.HTTPAddr)
		}),
		fx.Invoke(func(lifecycle fx.Lifecycle, shutdown fx.Shutdowner, server *http.Server, cfg config.Config) error {
			var err error
			managed, err := registerLifecycle(lifecycle, shutdown, server, checks, cfg, runner)
			if managedOut != nil {
				*managedOut = managed
			}
			return err
		}),
	}
	return append(options, extra...)
}

func validRuntimeConfig() config.Config {
	return config.Config{
		HTTPAddr: "127.0.0.1:0", DatabaseURL: "postgres://user:pass@localhost:5432/wagering",
		SQSEndpoint: "http://localhost:4566", SQSRegion: "us-east-1",
		OIDCIssuer: "http://localhost:8080/realms/wagering", OIDCJWKSURL: "http://localhost:8080/realms/wagering/certs",
		OIDCAudience: "wagering-api", ConsumerName: "consumer", ConsumerWorkers: 1, ConsumerPoll: time.Second,
		OutboxOwner: "publisher", OutboxBatch: 1, OutboxInterval: time.Second,
		RetryBatch: 1, RetryInterval: time.Second, ShutdownTimeout: time.Second,
	}
}
