package sqs

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	sqssdk "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

// Filas do serviço.
const (
	MainQueue   = "wager-transactions.fifo"
	DLQQueue    = "wager-transactions-dlq.fifo"
	EventsQueue = "wager-events.fifo"
)

// Client fala com o SQS (ou o MiniStack local).
type Client struct {
	endpoint string
	inner    *sqssdk.Client
	urls     map[string]string
}

// NewClient monta o cliente para o endpoint (ex.: http://localhost:4566).
// Credencial é de mentira no emulador; na AWS real vêm do ambiente.
func NewClient(ctx context.Context, endpoint, region string) (*Client, error) {
	if endpoint == "" || region == "" {
		return nil, fmt.Errorf("sqs: endpoint ou região vazios")
	}
	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion(region),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test", "test", "")),
		config.WithBaseEndpoint(endpoint),
	)
	if err != nil {
		return nil, fmt.Errorf("sqs: config: %w", err)
	}
	return &Client{endpoint: endpoint, inner: sqssdk.NewFromConfig(cfg, func(o *sqssdk.Options) {
		o.Region = region
		o.BaseEndpoint = aws.String(endpoint)
	})}, nil
}

// Name identifica o cheque de saúde.
func (c *Client) Name() string { return "sqs" }

// Check prova que a fila responde (lista com prazo curto).
func (c *Client) Check(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_, err := c.inner.ListQueues(ctx, &sqssdk.ListQueuesInput{})
	if err != nil {
		return fmt.Errorf("sqs %s: %w", c.endpoint, err)
	}
	return nil
}

// ResolveQueue descobre a URL da fila pelo nome (guarda para reusar).
func (c *Client) ResolveQueue(ctx context.Context, name string) (string, error) {
	if url, ok := c.urls[name]; ok {
		return url, nil
	}
	out, err := c.inner.GetQueueUrl(ctx, &sqssdk.GetQueueUrlInput{QueueName: aws.String(name)})
	if err != nil {
		return "", fmt.Errorf("sqs: fila %s: %w", name, err)
	}
	if c.urls == nil {
		c.urls = map[string]string{}
	}
	c.urls[name] = aws.ToString(out.QueueUrl)
	return c.urls[name], nil
}

// Sent é a mensagem enviada (id para rastrear a deduplicação).
type Sent struct {
	MessageID string
}

// Send publica na fila FIFO com grupo e deduplicação explícitos.
func (c *Client) Send(ctx context.Context, queueURL, body, groupID, dedupID string) (Sent, error) {
	out, err := c.inner.SendMessage(ctx, &sqssdk.SendMessageInput{
		QueueUrl:               aws.String(queueURL),
		MessageBody:            aws.String(body),
		MessageGroupId:         aws.String(groupID),
		MessageDeduplicationId: aws.String(dedupID),
	})
	if err != nil {
		return Sent{}, fmt.Errorf("sqs: enviando: %w", err)
	}
	return Sent{MessageID: aws.ToString(out.MessageId)}, nil
}

// Received é uma mensagem com o recibo para confirmar ou soltar.
type Received struct {
	MessageID     string
	ReceiptHandle string
	Body          string
	ReceiveCount  int
}

// Receive busca até max mensagens com espera longa.
func (c *Client) Receive(ctx context.Context, queueURL string, max, waitSeconds int) ([]Received, error) {
	out, err := c.inner.ReceiveMessage(ctx, &sqssdk.ReceiveMessageInput{
		QueueUrl:            aws.String(queueURL),
		MaxNumberOfMessages: int32(max),
		WaitTimeSeconds:     int32(waitSeconds),
		AttributeNames:      []types.QueueAttributeName{types.QueueAttributeName("ApproximateReceiveCount")},
	})
	if err != nil {
		return nil, fmt.Errorf("sqs: recebendo: %w", err)
	}
	var messages []Received
	for _, m := range out.Messages {
		count := 0
		if v, ok := m.Attributes["ApproximateReceiveCount"]; ok {
			count, _ = strconv.Atoi(v)
		}
		messages = append(messages, Received{
			MessageID: aws.ToString(m.MessageId), ReceiptHandle: aws.ToString(m.ReceiptHandle),
			Body: aws.ToString(m.Body), ReceiveCount: count,
		})
	}
	return messages, nil
}

// SendDLQ copia a mensagem arsenicada para a DLQ com o motivo, mantendo o identificador para deduplicar por lá também.
func (c *Client) SendDLQ(ctx context.Context, dlqURL, originalBody, reason, dedupID string) error {
	payload, err := json.Marshal(map[string]string{
		"reason": reason, "messageId": dedupID, "body": originalBody,
	})
	if err != nil {
		return fmt.Errorf("sqs: envelope da DLQ: %w", err)
	}
	_, err = c.inner.SendMessage(ctx, &sqssdk.SendMessageInput{
		QueueUrl:               aws.String(dlqURL),
		MessageBody:            aws.String(string(payload)),
		MessageGroupId:         aws.String("dlq"),
		MessageDeduplicationId: aws.String(dedupID),
	})
	if err != nil {
		return fmt.Errorf("sqs: para a DLQ: %w", err)
	}
	return nil
}

// Delete confirma a mensagem (só depois do commit no banco).
func (c *Client) Delete(ctx context.Context, queueURL, receiptHandle string) error {
	_, err := c.inner.DeleteMessage(ctx, &sqssdk.DeleteMessageInput{
		QueueUrl: aws.String(queueURL), ReceiptHandle: aws.String(receiptHandle),
	})
	if err != nil {
		return fmt.Errorf("sqs: confirmando: %w", err)
	}
	return nil
}

// Release solta a mensagem de volta (visível de novo) para outra tentativa.
func (c *Client) Release(ctx context.Context, queueURL, receiptHandle string, delaySeconds int) error {
	_, err := c.inner.ChangeMessageVisibility(ctx, &sqssdk.ChangeMessageVisibilityInput{
		QueueUrl: aws.String(queueURL), ReceiptHandle: aws.String(receiptHandle),
		VisibilityTimeout: int32(delaySeconds),
	})
	if err != nil {
		return fmt.Errorf("sqs: soltando: %w", err)
	}
	return nil
}
