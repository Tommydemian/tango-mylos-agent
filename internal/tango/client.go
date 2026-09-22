// Package tango es el cliente HTTP de la API Live de Tango on-premise.
// Solo implementa lo confirmado empiricamente: GET /Api/GetApiLiveQueryData.
package tango

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/mylos/mylos-tango-agent/internal/config"
	"github.com/mylos/mylos-tango-agent/internal/model"
)

// liveQueryPath es el unico endpoint que usamos hoy.
const liveQueryPath = "/Api/GetApiLiveQueryData"

// maxErrorBodySnippet limita cuanto del body de error se arrastra al mensaje.
const maxErrorBodySnippet = 512

// Client habla con un servidor Tango. Es seguro para uso concurrente.
type Client struct {
	baseURL   *url.URL
	token     string // SECRETO
	companyID string

	httpc *http.Client
	log   *slog.Logger

	maxRetries     int
	retryBaseDelay time.Duration
}

// New construye el cliente a partir de la config ya validada.
func New(cfg config.Config, log *slog.Logger) (*Client, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	u, err := url.Parse(strings.TrimRight(cfg.TangoBaseURL, "/"))
	if err != nil {
		return nil, fmt.Errorf("tango: base url invalida: %w", err)
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Client{
		baseURL:        u,
		token:          cfg.TangoAPIToken,
		companyID:      cfg.TangoCompanyID,
		httpc:          &http.Client{Timeout: cfg.HTTPTimeout},
		log:            log,
		maxRetries:     cfg.MaxRetries,
		retryBaseDelay: cfg.RetryBaseDelay,
	}, nil
}

// QueryParams son los parametros de GetApiLiveQueryData.
// PageIndex arranca en 0. CustomQuery es opcional.
type QueryParams struct {
	Process     string
	FromDate    string
	ToDate      string
	PageSize    int
	PageIndex   int
	CustomQuery string
}

func (p QueryParams) validate() error {
	if strings.TrimSpace(p.Process) == "" {
		return errors.New("tango: falta el parametro process")
	}
	if p.PageSize <= 0 {
		return errors.New("tango: pageSize debe ser > 0")
	}
	if p.PageIndex < 0 {
		return errors.New("tango: pageIndex no puede ser negativo")
	}
	return nil
}

// GetApiLiveQueryData pide UNA pagina de una consulta Live y la decodifica en T.
// Devuelve error si succeeded=false, si el status no es 2xx, o si el JSON no
// tiene la forma esperada. Reintenta errores transitorios (red, 5xx, 429).
func GetApiLiveQueryData[T any](ctx context.Context, c *Client, p QueryParams) (*model.Page[T], error) {
	if c == nil {
		return nil, errors.New("tango: cliente nil")
	}
	if err := p.validate(); err != nil {
		return nil, err
	}

	endpoint := c.baseURL.JoinPath(liveQueryPath)
	q := url.Values{}
	q.Set("process", p.Process)
	q.Set("fromDate", p.FromDate)
	q.Set("toDate", p.ToDate)
	q.Set("pageSize", strconv.Itoa(p.PageSize))
	q.Set("pageIndex", strconv.Itoa(p.PageIndex))
	if p.CustomQuery != "" {
		q.Set("customQuery", p.CustomQuery)
	}
	endpoint.RawQuery = q.Encode()

	attempts := c.maxRetries + 1
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		page, err := doOnce[T](ctx, c, endpoint.String(), attempt)
		if err == nil {
			return page, nil
		}
		lastErr = err

		if !retryable(err) || attempt == attempts || ctx.Err() != nil {
			break
		}
		delay := c.backoff(attempt)
		c.log.Warn("tango: reintentando",
			slog.Int("attempt", attempt),
			slog.Int("max_attempts", attempts),
			slog.Duration("delay", delay),
			slog.String("error", err.Error()),
		)
		if err := sleepCtx(ctx, delay); err != nil {
			return nil, fmt.Errorf("tango: cancelado durante el backoff: %w", err)
		}
	}
	return nil, lastErr
}

// decodeEnvelope decodifica el sobre de la API y devuelve la pagina tipada.
func decodeEnvelope[T any](body io.Reader) (*model.Page[T], error) {
	var env model.Envelope[T]
	dec := json.NewDecoder(body)
	if err := dec.Decode(&env); err != nil {
		return nil, &ResponseError{Reason: "no se pudo decodificar el JSON: " + err.Error()}
	}
	if !env.Succeeded {
		msg := ""
		if env.Message != nil {
			msg = *env.Message
		}
		return nil, &APIError{Message: msg, ExceptionInfo: rawToString(env.ExceptionInfo)}
	}
	if env.ResultData == nil {
		return nil, &ResponseError{Reason: "succeeded=true pero resultData vino null"}
	}
	return env.ResultData, nil
}

// doOnce ejecuta un intento: request, chequeo de status y decodificacion.
func doOnce[T any](ctx context.Context, c *Client, rawURL string, attempt int) (*model.Page[T], error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("tango: no se pudo armar el request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("ApiAuthorization", c.token) // SECRETO: no loguear este header
	req.Header.Set("Company", c.companyID)

	start := time.Now()
	c.log.Debug("tango: GET", slog.String("url", rawURL), slog.Int("attempt", attempt))

	resp, err := c.httpc.Do(req)
	if err != nil {
		// *url.Error trae metodo y URL, nunca headers: el token no se filtra.
		return nil, fmt.Errorf("tango: fallo la conexion con el servidor: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodySnippet))
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, &HTTPError{
			StatusCode: resp.StatusCode,
			Status:     resp.Status,
			// Si el server nos devuelve el token en el body (pasa con algunos
			// 401), no lo dejamos entrar al error ni, por lo tanto, al log.
			Body: c.redact(strings.TrimSpace(string(snippet))),
		}
	}

	page, err := decodeEnvelope[T](resp.Body)
	if err != nil {
		return nil, err
	}
	c.log.Debug("tango: respuesta ok",
		slog.String("url", rawURL),
		slog.Duration("elapsed", time.Since(start)),
	)
	return page, nil
}

// redact saca el token de cualquier texto que venga del server.
func (c *Client) redact(s string) string {
	if c.token == "" {
		return s
	}
	return strings.ReplaceAll(s, c.token, "[redacted]")
}

// backoff exponencial con jitter, acotado a 30s.
func (c *Client) backoff(attempt int) time.Duration {
	if c.retryBaseDelay <= 0 {
		return 0
	}
	d := c.retryBaseDelay << (attempt - 1)
	const maxDelay = 30 * time.Second
	if d > maxDelay || d <= 0 {
		d = maxDelay
	}
	jitter := time.Duration(rand.Int64N(int64(d)/2 + 1))
	return d/2 + jitter
}

// retryable decide si un error amerita reintento.
// Errores de transporte (timeout, conexion caida, DNS) si; HTTP 5xx/429/408 si;
// resto de 4xx, succeeded=false y JSON invalido no.
func retryable(err error) bool {
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		return httpErr.Retryable()
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return false
	}
	var respErr *ResponseError
	if errors.As(err, &respErr) {
		return false
	}
	// Timeouts y cortes de conexion llegan como *url.Error y si se reintentan.
	// La cancelacion del ctx del caller la corta el loop de GetApiLiveQueryData.
	var urlErr *url.Error
	return errors.As(err, &urlErr)
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func rawToString(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return ""
	}
	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		return asString
	}
	if len(s) > maxErrorBodySnippet {
		s = s[:maxErrorBodySnippet] + "..."
	}
	return s
}
