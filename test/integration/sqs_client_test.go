//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/google/uuid"

	infrasqs "github.com/marcosarl1/backend-challenge-go/internal/infra/sqs"
)

func sqsClient(t *testing.T) (*infrasqs.Client, string) {
	t.Helper()
	endpoint := sqsURL(t)
	client, err := infrasqs.NewClient(context.Background(), endpoint, "us-east-1")
	if err != nil {
		t.Fatalf("cliente: %v", err)
	}
	url, err := client.ResolveQueue(context.Background(), infrasqs.EventsQueue)
	if err != nil {
		t.Fatalf("fila: %v", err)
	}
	return client, url
}

// isolatedQueue cria uma fila própria para o teste, sem mensagens deixadas por outros cenários.
func isolatedQueue(t *testing.T, prefix string) string {
	t.Helper()
	ctx := context.Background()
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion("us-east-1"),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test", "test", "")),
		config.WithBaseEndpoint(sqsURL(t)))
	if err != nil {
		t.Fatalf("configurando fila: %v", err)
	}
	admin := awssqs.NewFromConfig(cfg)
	queueName := prefix + "-" + uuid.NewString() + ".fifo"
	queue, err := admin.CreateQueue(ctx, &awssqs.CreateQueueInput{QueueName: aws.String(queueName),
		Attributes: map[string]string{"FifoQueue": "true", "ContentBasedDeduplication": "false"}})
	if err != nil {
		t.Fatalf("fila isolada: %v", err)
	}
	url := aws.ToString(queue.QueueUrl)
	t.Cleanup(func() {
		if _, err := admin.DeleteQueue(context.Background(), &awssqs.DeleteQueueInput{QueueUrl: aws.String(url)}); err != nil {
			t.Errorf("apagando fila isolada: %v", err)
		}
	})
	return url
}

func TestSQSClientRoundTrip(t *testing.T) {
	ctx := context.Background()
	client, err := infrasqs.NewClient(ctx, sqsURL(t), "us-east-1")
	if err != nil {
		t.Fatalf("cliente: %v", err)
	}
	url := isolatedQueue(t, "roundtrip")
	dedup := "go-" + uuid.NewString()

	sent, err := client.Send(ctx, url, `{"teste":"testando"}`, "grupo-1", dedup)
	if err != nil {
		t.Fatalf("enviando: %v", err)
	}
	if sent.MessageID == "" {
		t.Fatal("sem id de mensagem")
	}
	// Dedup: reenvio com o mesmo id não duplica.
	if _, err := client.Send(ctx, url, `{"teste":"testando"}`, "grupo-1", dedup); err != nil {
		t.Fatalf("reenvio: %v", err)
	}

	received, err := client.Receive(ctx, url, 10, 5)
	if err != nil {
		t.Fatalf("recebendo: %v", err)
	}
	if len(received) != 1 || received[0].Body != `{"teste":"testando"}` || received[0].ReceiveCount < 1 {
		t.Fatalf("recebidas = %+v", received)
	}
	// Solta e recebe de novo (conta sobe), depois confirma e some.
	if err := client.Release(ctx, url, received[0].ReceiptHandle, 0); err != nil {
		t.Fatalf("soltando: %v", err)
	}
	again, err := client.Receive(ctx, url, 10, 5)
	if err != nil || len(again) != 1 || again[0].ReceiveCount < 2 {
		t.Fatalf("de novo = %+v, %v", again, err)
	}
	if err := client.Delete(ctx, url, again[0].ReceiptHandle); err != nil {
		t.Fatalf("confirmando: %v", err)
	}
	time.Sleep(2 * time.Second)
	empty, err := client.Receive(ctx, url, 10, 2)
	if err != nil || len(empty) != 0 {
		t.Fatalf("fila não esvaziou: %+v, %v", empty, err)
	}
}
