package observability

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"

	"go.uber.org/fx"
)

// Module monta o log em JSON (stdlib, sem dependência externa).
var Module = fx.Module("observability", fx.Provide(NewLogger, NewMetrics, NewTracing))

// NewLogger entrega o JSON para a saída padrão.
func NewLogger() *slog.Logger {
	return slog.New(NewJSONHandler(os.Stdout))
}

// NewJSONHandler monta um handler JSON que inclui campos do contexto e oculta dados sensíveis.
func NewJSONHandler(w io.Writer) slog.Handler {
	return contextHandler{next: slog.NewJSONHandler(w, nil)}
}

// contextHandler acrescenta campos do contexto antes de serializar cada registro.
type contextHandler struct{ next slog.Handler }

// Enabled consulta o nível configurado no handler JSON.
func (h contextHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

// Handle combina os campos do contexto com os do registro, dando prioridade aos últimos.
func (h contextHandler) Handle(ctx context.Context, record slog.Record) error {
	out := slog.NewRecord(record.Time, record.Level, record.Message, record.PC)
	attrs := make([]slog.Attr, 0, record.NumAttrs()+len(contextFields(ctx)))
	indices := make(map[string]int, cap(attrs))
	add := func(attr slog.Attr) {
		attr = redact(attr)
		if index, ok := indices[attr.Key]; ok {
			attrs[index] = attr
			return
		}
		indices[attr.Key] = len(attrs)
		attrs = append(attrs, attr)
	}
	for _, attr := range contextFields(ctx) {
		add(attr)
	}
	record.Attrs(func(attr slog.Attr) bool {
		add(attr)
		return true
	})
	out.AddAttrs(attrs...)
	return h.next.Handle(ctx, out)
}

// WithAttrs oculta atributos fixos antes de anexá-los ao handler JSON.
func (h contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	safe := make([]slog.Attr, len(attrs))
	for i, attr := range attrs {
		safe[i] = redact(attr)
	}
	return contextHandler{next: h.next.WithAttrs(safe)}
}

// WithGroup mantém o agrupamento dos próximos atributos.
func (h contextHandler) WithGroup(name string) slog.Handler {
	return contextHandler{next: h.next.WithGroup(name)}
}

type fieldsKey struct{}

// WithFields acrescenta identificadores ao contexto usado pelos logs.
func WithFields(ctx context.Context, attrs ...slog.Attr) context.Context {
	fields := append([]slog.Attr(nil), contextFields(ctx)...)
	for _, attr := range attrs {
		found := false
		for i := range fields {
			if fields[i].Key == attr.Key {
				fields[i], found = attr, true
				break
			}
		}
		if !found {
			fields = append(fields, attr)
		}
	}
	return context.WithValue(ctx, fieldsKey{}, fields)
}

// contextFields recupera os campos adicionados ao contexto da operação.
func contextFields(ctx context.Context) []slog.Attr {
	fields, _ := ctx.Value(fieldsKey{}).([]slog.Attr)
	return fields
}

// redact oculta valores sensíveis, inclusive dentro de grupos.
func redact(attr slog.Attr) slog.Attr {
	attr.Value = attr.Value.Resolve()
	if sensitive(attr.Key) {
		attr.Value = slog.StringValue("[REDACTED]")
		return attr
	}
	if attr.Value.Kind() == slog.KindGroup {
		attrs := append([]slog.Attr(nil), attr.Value.Group()...)
		for i := range attrs {
			attrs[i] = redact(attrs[i])
		}
		attr.Value = slog.GroupValue(attrs...)
	}
	return attr
}

// sensitive reconhece nomes de campos que não podem aparecer nos logs.
func sensitive(key string) bool {
	switch strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "_", ""), "-", "")) {
	case "authorization", "proxyauthorization", "cookie", "setcookie", "token", "accesstoken", "refreshtoken", "jwt",
		"password", "passwd", "secret", "clientsecret", "apikey", "privatekey", "credential", "credentials", "accesskey", "secretaccesskey", "panic",
		"body", "requestbody", "responsebody", "messagebody", "payload", "money", "amount", "initialbalance", "balance", "resultbalance", "balancebefore", "balanceafter",
		"stored", "storedbalance", "calculated", "calculatedbalance", "difference", "idempotencykey", "databaseurl":
		return true
	default:
		return false
	}
}
