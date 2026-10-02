package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/marcosarl1/backend-challenge-go/internal/application"
	"github.com/marcosarl1/backend-challenge-go/internal/domain/money"
	"github.com/marcosarl1/backend-challenge-go/internal/domain/wager"
	"github.com/marcosarl1/backend-challenge-go/internal/infra/auth"
	"github.com/marcosarl1/backend-challenge-go/internal/platform/observability"
)

// Server expõe os casos de uso em HTTP. Handlers finos: parseiam, chamam e
// traduzem; regra mora na aplicação.
type Server struct {
	uow    application.UnitOfWork
	auth   Authenticator
	clock  application.Clock
	ids    application.IDGenerator
	logger *slog.Logger
	checks []HealthChecker
}

// HealthChecker é uma dependência que a prontidão confere.
type HealthChecker interface {
	Name() string
	Check(ctx context.Context) error
}

// New monta o servidor sobre as dependências.
func New(uow application.UnitOfWork, auth Authenticator, clock application.Clock, ids application.IDGenerator, checks ...HealthChecker) *Server {
	return NewWithLogger(uow, auth, clock, ids, slog.Default(), checks...)
}

// NewWithLogger monta o servidor com logger estruturado.
func NewWithLogger(uow application.UnitOfWork, auth Authenticator, clock application.Clock, ids application.IDGenerator, logger *slog.Logger, checks ...HealthChecker) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{uow: uow, auth: auth, clock: clock, ids: ids, logger: logger, checks: checks}
}

// NewHTTPServer monta o servidor HTTP com prazos: cabeçalho lento não prende
// conexão, resposta lenta demais é cortada, ociosa recicla.
func NewHTTPServer(handler http.Handler, addr string) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
}

// Handler monta as rotas: saúde pública, negócio autenticado.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", s.handleLive)
	mux.HandleFunc("GET /health/ready", s.handleReady)

	biz := http.NewServeMux()
	biz.HandleFunc("POST /wallets", s.handleOpenWallet)
	biz.HandleFunc("GET /wallets/{walletId}", s.handleGetWallet)
	biz.HandleFunc("GET /wallets/{walletId}/ledger", s.handleListLedger)
	biz.HandleFunc("POST /wallets/{walletId}/reconciliation", s.handleReconcile)
	biz.HandleFunc("POST /wagering/transactions", s.handleProcess)
	biz.HandleFunc("GET /wagering/transactions/{transactionId}", s.handleGetTransaction)
	biz.HandleFunc("GET /providers/{providerId}/wagering/transactions/{externalTransactionId}", s.handleGetByExternal)
	mux.Handle("/", s.withAuth(biz))
	return s.withCorrelation(s.withRecover(mux))
}

// withCorrelation carrega ou gera o id de correlação (vai e volta no header,
// entra no corpo de erro e no contexto).
func (s *Server) withCorrelation(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		corr := r.Header.Get("X-Correlation-Id")
		if corr == "" {
			corr = uuid.NewString()
		}
		w.Header().Set("X-Correlation-Id", corr)
		ctx := observability.WithFields(r.Context(), slog.String("correlationId", corr))
		next.ServeHTTP(w, r.WithContext(context.WithValue(ctx, ctxKey("correlation"), corr)))
	})
}

// errPanic é o erro interno quando um handler estoura.
var errPanic = errors.New("falha interna")

// withRecover converte pânicos do handler em erro interno.
func (s *Server) withRecover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				s.logger.ErrorContext(r.Context(), "pânico no handler",
					"path", r.URL.Path, "panic", recovered)
				writeError(w, r, errPanic)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// handleLive diz que o processo está de pé (público, sem dependência).
func (s *Server) handleLive(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "live"})
}

// handleReady confere as dependências (público): banco e fila respondendo é
// 200; qualquer uma fora é 503 com quem falhou.
func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	var failing []string
	for _, check := range s.checks {
		if err := check.Check(ctx); err != nil {
			failing = append(failing, check.Name())
		}
	}
	if len(failing) > 0 {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"status": "not-ready", "failing": failing,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ready"})
}

// withAuth exige Bearer válido e guarda a identidade no contexto.
func (s *Server) withAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := r.Header.Get("Authorization")
		token, ok := strings.CutPrefix(raw, "Bearer ")
		if !ok || token == "" {
			writeError(w, r, &authError{err: auth.ErrMissingToken})
			return
		}
		principal, err := s.auth.Authenticate(r.Context(), token)
		if err != nil {
			writeError(w, r, err)
			return
		}
		ctx := context.WithValue(r.Context(), identityKey, principal.Identity())
		if principal.ProviderID != "" {
			ctx = observability.WithFields(ctx, slog.String("providerId", principal.ProviderID))
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// authError embrulha o erro de autenticação para o mapeamento.
type authError struct{ err error }

func (e *authError) Error() string { return e.err.Error() }
func (e *authError) Unwrap() error { return e.err }

func (s *Server) handleOpenWallet(w http.ResponseWriter, r *http.Request) {
	var body OpenWalletRequest
	if err := decode(r, &body); err != nil {
		writeError(w, r, err)
		return
	}
	playerID, err := parseUUID("playerId", body.PlayerID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	balance, err := body.InitialBalance.parse()
	if err != nil {
		writeError(w, r, application.ErrInvalidInput)
		return
	}
	res, err := application.OpenWallet(r.Context(), s.uow, s.clock, s.ids, identityOf(r),
		application.OpenWalletCommand{PlayerID: playerID, InitialBalance: balance, CorrelationID: correlationOf(r)})
	if err != nil {
		writeError(w, r, err)
		return
	}
	r = withLogFields(r, slog.String("walletId", res.WalletID.String()))
	s.logger.InfoContext(r.Context(), "carteira aberta")
	writeJSON(w, http.StatusCreated, map[string]any{
		"id": res.WalletID.String(), "playerId": playerID.String(),
		"balance": moneyDTO(res.Balance), "version": res.Version,
	})
}

func (s *Server) handleGetWallet(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID("walletId", r.PathValue("walletId"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	r = withLogFields(r, slog.String("walletId", id.String()))
	view, err := application.GetWallet(r.Context(), s.uow, identityOf(r), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": view.ID.String(), "playerId": view.PlayerID.String(),
		"balance": moneyDTO(view.Balance), "version": view.Version,
	})
}

func (s *Server) handleListLedger(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID("walletId", r.PathValue("walletId"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	r = withLogFields(r, slog.String("walletId", id.String()))
	limit := 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil {
			writeError(w, r, application.ErrInvalidInput)
			return
		}
	}
	page, err := application.ListLedger(r.Context(), s.uow, identityOf(r), id, r.URL.Query().Get("cursor"), limit)
	if err != nil {
		writeError(w, r, err)
		return
	}
	entries := make([]any, 0, len(page.Entries))
	for _, e := range page.Entries {
		entries = append(entries, map[string]any{
			"id": e.ID.String(), "walletId": e.WalletID.String(), "transactionId": e.TransactionID.String(),
			"direction": e.Direction, "money": moneyDTO(e.Amount),
			"balanceBefore": moneyDTO(e.BalanceBefore), "balanceAfter": moneyDTO(e.BalanceAfter),
			"createdAt": e.CreatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries, "nextCursor": page.NextCursor})
}

func (s *Server) handleReconcile(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID("walletId", r.PathValue("walletId"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	r = withLogFields(r, slog.String("walletId", id.String()))
	rep, err := application.Reconcile(r.Context(), s.uow, s.logger, identityOf(r), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"walletId":      rep.WalletID.String(),
		"storedBalance": moneyDTO(rep.Stored), "calculatedBalance": moneyDTO(rep.Calculated),
		"difference": moneyDTO(rep.Difference), "consistent": rep.Consistent,
		"checkedEntries": rep.CheckedEntries,
	})
}

func (s *Server) handleProcess(w http.ResponseWriter, r *http.Request) {
	var body ProcessRequest
	if err := decode(r, &body); err != nil {
		writeError(w, r, err)
		return
	}
	cmd, err := body.command(r.Header.Get("Idempotency-Key"), correlationOf(r))
	if err != nil {
		writeError(w, r, err)
		return
	}
	r = withLogFields(r,
		slog.String("providerId", cmd.ProviderID),
		slog.String("walletId", cmd.WalletID.String()),
	)
	res, err := application.Execute(r.Context(), s.uow, s.clock, s.ids, identityOf(r), cmd)
	if err != nil {
		writeError(w, r, err)
		return
	}
	r = withLogFields(r, slog.String("transactionId", res.TransactionID.String()))
	s.logger.InfoContext(r.Context(), "operação processada",
		"kind", string(cmd.Kind), "status", string(res.Status), "idempotentReplay", res.IdempotentReplay)
	out := map[string]any{
		"transactionId": res.TransactionID.String(), "status": string(res.Status),
		"balance": moneyDTO(res.Balance), "idempotentReplay": res.IdempotentReplay,
	}
	switch res.Status {
	case wager.StatusProcessed:
		writeJSON(w, http.StatusOK, out)
	case wager.StatusRejected, wager.StatusFailed:
		out["failureCode"] = string(res.FailureCode)
		writeJSON(w, http.StatusUnprocessableEntity, out)
	default:
		w.Header().Set("Location", "/wagering/transactions/"+res.TransactionID.String())
		w.Header().Set("Retry-After", "2")
		writeJSON(w, http.StatusAccepted, out)
	}
}

func (s *Server) handleGetTransaction(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID("transactionId", r.PathValue("transactionId"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	r = withLogFields(r, slog.String("transactionId", id.String()))
	view, err := application.GetTransaction(r.Context(), s.uow, identityOf(r), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, transactionDTO(view))
}

func (s *Server) handleGetByExternal(w http.ResponseWriter, r *http.Request) {
	r = withLogFields(r, slog.String("providerId", r.PathValue("providerId")))
	view, err := application.GetTransactionByExternal(r.Context(), s.uow, identityOf(r),
		r.PathValue("providerId"), r.PathValue("externalTransactionId"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, transactionDTO(view))
}

func moneyDTO(m money.Money) map[string]any {
	return map[string]any{"amount": m.String(), "currency": string(m.Currency())}
}

// withLogFields acrescenta identificadores ao contexto da requisição.
func withLogFields(r *http.Request, attrs ...slog.Attr) *http.Request {
	return r.WithContext(observability.WithFields(r.Context(), attrs...))
}

func transactionDTO(view *application.TransactionView) map[string]any {
	out := map[string]any{
		"transactionId": view.TransactionID.String(), "providerId": view.ProviderID,
		"externalTransactionId": view.ExternalID, "walletId": view.WalletID.String(),
		"playerId": view.PlayerID.String(), "roundId": view.RoundID, "gameId": view.GameID,
		"kind": string(view.Kind), "money": moneyDTO(view.Amount), "status": string(view.Status),
	}
	if view.ReferenceExternalID != "" {
		out["referenceExternalTransactionId"] = view.ReferenceExternalID
	}
	if view.FailureCode != "" {
		out["failureCode"] = string(view.FailureCode)
		out["failureDetail"] = view.FailureDetail
	}
	if view.HasResult {
		out["resultBalance"] = moneyDTO(view.ResultBalance)
		out["resultWalletVersion"] = view.ResultWalletVersion
	}
	if view.Attempts > 0 {
		out["attempts"] = view.Attempts
	}
	if !view.NextAttemptAt.IsZero() {
		out["nextAttemptAt"] = view.NextAttemptAt.UTC().Format(time.RFC3339Nano)
		out["expiresAt"] = view.ExpiresAt.UTC().Format(time.RFC3339Nano)
	}
	return out
}
