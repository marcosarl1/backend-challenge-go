//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	postgrescontainer "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	infrasqs "github.com/marcosarl1/backend-challenge-go/internal/infra/sqs"
	"github.com/marcosarl1/backend-challenge-go/migrations"
)

var integrationBinary string

// TestMain cria dependências isoladas, aplica o schema e compila o processo real uma vez.
func TestMain(m *testing.M) {
	// O filho dos testes de crash reutiliza a infraestrutura do pai, sem abrir outros containers.
	if os.Getenv("WAGER_RECOVERY_CHILD") != "" {
		os.Exit(m.Run())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	cleanup, err := setupIntegration(ctx)
	if err != nil {
		cancel()
		fmt.Fprintln(os.Stderr, "infra de integração:", err)
		os.Exit(1)
	}
	code := m.Run()
	cleanup()
	cancel()
	os.Exit(code)
}

// setupIntegration prepara o binário e os três serviços reais para toda a suíte.
func setupIntegration(ctx context.Context) (func(), error) {
	dir, err := os.MkdirTemp("", "wager-integration-*")
	if err != nil {
		return nil, err
	}
	var containers []testcontainers.Container
	cleanup := func() {
		for i := len(containers) - 1; i >= 0; i-- {
			if err := testcontainers.TerminateContainer(containers[i]); err != nil {
				fmt.Fprintln(os.Stderr, "encerrando container:", err)
			}
		}
		_ = os.RemoveAll(dir)
	}
	fail := func(err error) (func(), error) { cleanup(); return nil, err }

	integrationBinary = filepath.Join(dir, "wagering")
	build := exec.CommandContext(ctx, "go", "build", "-race", "-o", integrationBinary, "./cmd/wagering")
	build.Dir = filepath.Join("..", "..")
	if output, err := build.CombinedOutput(); err != nil {
		return fail(fmt.Errorf("compilando binário: %w: %s", err, output))
	}

	db, err := postgrescontainer.Run(ctx, "postgres:17.11-alpine",
		postgrescontainer.WithDatabase("wagering"), postgrescontainer.WithUsername("wagering"),
		postgrescontainer.WithPassword("wagering"), postgrescontainer.BasicWaitStrategies())
	if db != nil {
		containers = append(containers, db)
	}
	if err != nil {
		return fail(fmt.Errorf("iniciando PostgreSQL: %w", err))
	}
	dbURL, err := db.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		return fail(err)
	}
	if err := applyMigrations(ctx, dbURL); err != nil {
		return fail(err)
	}

	realmPath, err := filepath.Abs(filepath.Join("..", "..", "deploy", "keycloak", "realm-export.json"))
	if err != nil {
		return fail(err)
	}
	keycloak, err := testcontainers.Run(ctx, "quay.io/keycloak/keycloak:26.7.5",
		testcontainers.WithCmd("start-dev", "--import-realm"),
		testcontainers.WithEnv(map[string]string{"KC_HOSTNAME_STRICT": "false", "KC_BOOTSTRAP_ADMIN_USERNAME": "admin", "KC_BOOTSTRAP_ADMIN_PASSWORD": "admin"}),
		testcontainers.WithFiles(testcontainers.ContainerFile{HostFilePath: realmPath, ContainerFilePath: "/opt/keycloak/data/import/realm.json", FileMode: 0644}),
		testcontainers.WithExposedPorts("8080/tcp"),
		testcontainers.WithWaitStrategy(wait.ForHTTP("/realms/wagering/.well-known/openid-configuration").WithPort("8080/tcp").WithStartupTimeout(3*time.Minute)))
	if keycloak != nil {
		containers = append(containers, keycloak)
	}
	if err != nil {
		return fail(fmt.Errorf("iniciando Keycloak: %w", err))
	}
	keycloakURL, err := keycloak.PortEndpoint(ctx, "8080/tcp", "http")
	if err != nil {
		return fail(err)
	}

	sqs, err := testcontainers.Run(ctx, "ministackorg/ministack:1.5.20",
		testcontainers.WithEnv(map[string]string{"AWS_REGION": "us-east-1"}),
		testcontainers.WithExposedPorts("4566/tcp"),
		testcontainers.WithWaitStrategy(wait.ForHTTP("/_localstack/health").WithPort("4566/tcp").WithStartupTimeout(90*time.Second)))
	if sqs != nil {
		containers = append(containers, sqs)
	}
	if err != nil {
		return fail(fmt.Errorf("iniciando MiniStack: %w", err))
	}
	sqsURL, err := sqs.PortEndpoint(ctx, "4566/tcp", "http")
	if err != nil {
		return fail(err)
	}
	if err := createQueues(ctx, sqsURL); err != nil {
		return fail(err)
	}
	for key, value := range map[string]string{
		"TEST_DATABASE_URL": dbURL, "TEST_KEYCLOAK_URL": keycloakURL, "TEST_SQS_ENDPOINT": sqsURL,
	} {
		if err := os.Setenv(key, value); err != nil {
			return fail(err)
		}
	}
	return cleanup, nil
}

// applyMigrations executa os arquivos embarcados na ordem das versões.
func applyMigrations(ctx context.Context, databaseURL string) error {
	conn, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("conectando para migrations: %w", err)
	}
	defer conn.Close(context.Background())
	files, err := fs.Glob(migrations.FS, "*.up.sql")
	if err != nil {
		return err
	}
	for _, name := range files {
		query, err := fs.ReadFile(migrations.FS, name)
		if err != nil {
			return err
		}
		if _, err := conn.Exec(ctx, string(query), pgx.QueryExecModeSimpleProtocol); err != nil {
			return fmt.Errorf("migration %s: %w", name, err)
		}
	}
	return nil
}

// createQueues replica as filas FIFO e o redrive usados pelo compose local.
func createQueues(ctx context.Context, endpoint string) error {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion("us-east-1"),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test", "test", "")),
		config.WithBaseEndpoint(endpoint))
	if err != nil {
		return err
	}
	client := awssqs.NewFromConfig(cfg)
	create := func(name string) (string, error) {
		out, err := client.CreateQueue(ctx, &awssqs.CreateQueueInput{QueueName: aws.String(name),
			Attributes: map[string]string{"FifoQueue": "true", "ContentBasedDeduplication": "false"}})
		if err != nil {
			return "", fmt.Errorf("criando fila %s: %w", name, err)
		}
		return aws.ToString(out.QueueUrl), nil
	}
	dlqURL, err := create(infrasqs.DLQQueue)
	if err != nil {
		return err
	}
	if _, err := create(infrasqs.EventsQueue); err != nil {
		return err
	}
	mainURL, err := create(infrasqs.MainQueue)
	if err != nil {
		return err
	}
	attrs, err := client.GetQueueAttributes(ctx, &awssqs.GetQueueAttributesInput{
		QueueUrl: aws.String(dlqURL), AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameQueueArn}})
	if err != nil {
		return err
	}
	policy, err := json.Marshal(map[string]string{"deadLetterTargetArn": attrs.Attributes["QueueArn"], "maxReceiveCount": "5"})
	if err != nil {
		return err
	}
	_, err = client.SetQueueAttributes(ctx, &awssqs.SetQueueAttributesInput{QueueUrl: aws.String(mainURL),
		Attributes: map[string]string{"VisibilityTimeout": "30", "RedrivePolicy": string(policy)}})
	return err
}

// TestBinaryProcessStartsWithRealDependencies sobe o executável e verifica o readiness real.
func TestBinaryProcessStartsWithRealDependencies(t *testing.T) {
	base := startAppProcess(t)
	response, err := http.Get(base + "/health/ready")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("readiness = %d", response.StatusCode)
	}
}

// startAppProcess inicia uma instância em porta própria e a encerra no cleanup.
func startAppProcess(t *testing.T) string {
	base, _ := startAppProcessControlled(t)
	return base
}

// startAppProcessControlled inicia uma instância que o teste também pode encerrar antes do cleanup.
func startAppProcessControlled(t *testing.T, extraEnv ...string) (string, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	listener.Close()
	logFile, err := os.Create(filepath.Join(t.TempDir(), "app.log"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(integrationBinary)
	cmd.Env = append(append(os.Environ(), "HTTP_ADDR="+addr, "METRICS_ADDR=127.0.0.1:0",
		"DATABASE_URL="+os.Getenv("TEST_DATABASE_URL"), "SQS_ENDPOINT="+os.Getenv("TEST_SQS_ENDPOINT"),
		"OIDC_ISSUER="+os.Getenv("TEST_KEYCLOAK_URL")+"/realms/wagering",
		"OIDC_JWKS_URL="+os.Getenv("TEST_KEYCLOAK_URL")+"/realms/wagering/protocol/openid-connect/certs",
		"OUTBOX_OWNER=integration-"+uuid.NewString()), extraEnv...)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		logFile.Close()
		t.Fatal(err)
	}
	finished := make(chan struct{})
	var waitErr error
	go func() { waitErr = cmd.Wait(); close(finished) }()
	var stopOnce sync.Once
	stop := func() {
		stopOnce.Do(func() {
			select {
			case <-finished:
			default:
				_ = cmd.Process.Signal(syscall.SIGTERM)
			}
			select {
			case <-finished:
				if waitErr != nil {
					t.Errorf("encerrando processo: %v", waitErr)
				}
			case <-time.After(20 * time.Second):
				_ = cmd.Process.Kill()
				<-finished
				t.Error("processo não encerrou após SIGTERM")
			}
			logFile.Close()
		})
	}
	t.Cleanup(stop)
	base := "http://" + addr
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		response, err := http.Get(base + "/health/live")
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return base, stop
			}
		}
		select {
		case <-finished:
			data, _ := os.ReadFile(logFile.Name())
			t.Fatalf("processo terminou antes do liveness: %v: %s", waitErr, data)
		default:
		}
		time.Sleep(100 * time.Millisecond)
	}
	data, _ := os.ReadFile(logFile.Name())
	t.Fatalf("liveness não respondeu: %s", data)
	return "", stop
}
