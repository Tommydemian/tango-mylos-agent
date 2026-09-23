// Package mylos es el cliente HTTP de la API de ingesta de MYLOS.
//
// El agente es transporte: manda las filas tal cual vinieron de Tango. No
// mapea, no transforma, no interpreta campos. El backend guarda el raw y
// resuelve el negocio.
package mylos

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mylos/mylos-tango-agent/internal/config"
)

// maxErrorBodySnippet limita cuanto del body de error se arrastra al mensaje.
const maxErrorBodySnippet = 512

// Dataset es cada una de las ingestas que expone MYLOS.
type Dataset string

const (
	DatasetCustomers Dataset = "customers"
	DatasetSales     Dataset = "sales"
)

// Path es el endpoint de ingesta del dataset.
func (d Dataset) Path() string { return "/integrations/tango/" + string(d) + "/batch" }

// Batch es el cuerpo que espera MYLOS. Rows va tal cual salio de Tango.
//
// No lleva tenant: el backend lo resuelve por el token.
type Batch struct {
	CompanyID     int               `json:"company_id"`
	ProcessID     int               `json:"process_id"`
	CustomQueryID string            `json:"custom_query_id"`
	FromDate      string            `json:"from_date"`
	ToDate        string            `json:"to_date"`
	Rows          []json.RawMessage `json:"rows"`
}

// BatchResponse es lo que contesta MYLOS.
//
// duplicate=true con stored=false NO es un error: significa que el backend ya
// tenia ese batch (idempotencia por payload_hash) y lo descarto.
type BatchResponse struct {
	BatchID   string `json:"batch_id"`
	Received  int    `json:"received"`
	Stored    bool   `json:"stored"`
	Duplicate bool   `json:"duplicate"`
}

// Client habla con la API de MYLOS. Es seguro para uso concurrente.
type Client struct {
	baseURL *url.URL
	token   string // SECRETO
	httpc   *http.Client
	log     *slog.Logger

	maxRetries     int
	retryBaseDelay time.Duration
}

// New construye el cliente y valida que la config de MYLOS este completa.
func New(cfg config.Config, log *slog.Logger) (*Client, error) {
	var faltan []string
	if cfg.MylosBaseURL == "" {
		faltan = append(faltan, "MYLOS_BASE_URL")
	}
	if cfg.MylosIngestToken == "" {
		faltan = append(faltan, "MYLOS_INGEST_TOKEN")
	}
	if len(faltan) > 0 {
		return nil, fmt.Errorf("mylos: faltan variables obligatorias para enviar a MYLOS: %s",
			strings.Join(faltan, ", "))
	}

	u, err := url.Parse(strings.TrimRight(cfg.MylosBaseURL, "/"))
	if err != nil {
		return nil, fmt.Errorf("mylos: MYLOS_BASE_URL invalida: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("mylos: MYLOS_BASE_URL debe ser http:// o https:// (es %q)", u.Scheme)
	}
	if u.Host == "" {
		return nil, errors.New("mylos: MYLOS_BASE_URL sin host")
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Client{
		baseURL:        u,
		token:          cfg.MylosIngestToken,
		httpc:          &http.Client{Timeout: cfg.MylosHTTPTimeout},
		log:            log,
		maxRetries:     cfg.MylosMaxRetries,
		retryBaseDelay: cfg.MylosRetryBaseDelay,
	}, nil
}

// Target es la URL del endpoint, sin credenciales. Sirve para loguear a donde
// se esta mandando sin exponer nada.
func (c *Client) Target(d Dataset) string {
	return c.baseURL.JoinPath(d.Path()).String()
}

// SendBatch manda un batch y devuelve la respuesta de MYLOS.
//
// Reintenta errores transitorios (red, 408, 429, 5xx). Reintentar un POST es
// seguro *solo* porque el backend es idempotente por payload_hash: si el
// primer intento llego pero se perdio la respuesta, el reintento vuelve con
// duplicate=true en vez de insertar dos veces.
func (c *Client) SendBatch(ctx context.Context, d Dataset, b Batch) (*BatchResponse, error) {
	body, err := json.Marshal(b)
	if err != nil {
		return nil, fmt.Errorf("mylos: no se pudo serializar el batch: %w", err)
	}
	target := c.Target(d)

	attempts := c.maxRetries + 1
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		resp, err := c.doOnce(ctx, target, body, attempt)
		if err == nil {
			return resp, nil
		}
		lastErr = err

		if !retryable(err) || attempt == attempts || ctx.Err() != nil {
			break
		}
		delay := c.backoff(attempt)
		c.log.Warn("mylos: reintentando el envio",
			slog.String("target", target),
			slog.Int("attempt", attempt),
			slog.Int("max_attempts", attempts),
			slog.Duration("delay", delay),
			slog.String("error", err.Error()),
		)
		if err := sleepCtx(ctx, delay); err != nil {
			return nil, fmt.Errorf("mylos: cancelado durante el backoff: %w", err)
		}
	}
	return nil, lastErr
}

func (c *Client) doOnce(ctx context.Context, target string, body []byte, attempt int) (*BatchResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("mylos: no se pudo armar el request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token) // SECRETO: no loguear

	c.log.Debug("mylos: POST",
		slog.String("target", target),
		slog.Int("bytes", len(body)),
		slog.Int("attempt", attempt),
	)

	resp, err := c.httpc.Do(req)
	if err != nil {
		// *url.Error trae metodo y URL, nunca headers: el token no se filtra.
		return nil, fmt.Errorf("mylos: fallo la conexion con MYLOS: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodySnippet))
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, &HTTPError{
			StatusCode: resp.StatusCode,
			Status:     resp.Status,
			// Si el backend devuelve el token en el body, no lo dejamos
			// entrar al error ni, por lo tanto, al log.
			Body: c.redact(strings.TrimSpace(string(snippet))),
		}
	}

	var out BatchResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, &ResponseError{Reason: "no se pudo decodificar la respuesta: " + err.Error()}
	}
	return &out, nil
}

func (c *Client) redact(s string) string {
	if c.token == "" {
		return s
	}
	return strings.ReplaceAll(s, c.token, "[redacted]")
}

// backoff exponencial con jitter, acotado a 30s.
// Mismo criterio que internal/tango: si cambia uno, revisar el otro.
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

// retryable: transporte, 408, 429 y 5xx si; el resto de 4xx y una respuesta
// ilegible no. La cancelacion del ctx la corta el loop de SendBatch.
func retryable(err error) bool {
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		return httpErr.Retryable()
	}
	var respErr *ResponseError
	if errors.As(err, &respErr) {
		return false
	}
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
