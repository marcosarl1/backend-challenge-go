package sqs

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	sqssdk "github.com/aws/aws-sdk-go-v2/service/sqs"
)

// Client fala com o SQS (ou o MiniStack local). Começa com o mínimo que a
// saúde precisa; consumidor e publicador crescem aqui.
type Client struct {
	endpoint string
	inner    *sqssdk.Client
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
