// Package config carga la configuracion del agente desde variables de entorno
// (opcionalmente precargadas desde un archivo tipo .env). No hay defaults con
// secretos ni valores hardcodeados de ningun cliente.
package config

import (
	"bufio"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config es la configuracion efectiva del agente.
type Config struct {
	TangoBaseURL        string // ej: http://arenales_tango:17000
	TangoAPIToken       string // SECRETO: nunca loguear ni incluir en errores
	TangoCompanyID      string
	TangoSalesProcessID string

	HTTPTimeout    time.Duration
	MaxRetries     int
	RetryBaseDelay time.Duration

	// DateFormat es el layout Go con el que se serializan fromDate/toDate.
	// Default 02/01/2006 (dd/MM/yyyy), que es lo observado en Tango.
	// Sigue siendo configurable por si algun server espera otra cosa.
	DateFormat string
}

const (
	defaultHTTPTimeout    = 60 * time.Second
	defaultMaxRetries     = 3
	defaultRetryBaseDelay = 500 * time.Millisecond
	// Formato observado empiricamente en Tango (dd/MM/yyyy).
	defaultDateFormat = "02/01/2006"
)

// Load arma la Config desde el entorno del proceso y la valida.
func Load() (Config, error) {
	c := Config{
		TangoBaseURL:        strings.TrimSpace(os.Getenv("TANGO_BASE_URL")),
		TangoAPIToken:       strings.TrimSpace(os.Getenv("TANGO_API_TOKEN")),
		TangoCompanyID:      strings.TrimSpace(os.Getenv("TANGO_COMPANY_ID")),
		TangoSalesProcessID: strings.TrimSpace(os.Getenv("TANGO_SALES_PROCESS_ID")),
		HTTPTimeout:         defaultHTTPTimeout,
		MaxRetries:          defaultMaxRetries,
		RetryBaseDelay:      defaultRetryBaseDelay,
		DateFormat:          defaultDateFormat,
	}

	var err error
	if c.HTTPTimeout, err = durationEnv("TANGO_HTTP_TIMEOUT", defaultHTTPTimeout); err != nil {
		return Config{}, err
	}
	if c.MaxRetries, err = intEnv("TANGO_MAX_RETRIES", defaultMaxRetries); err != nil {
		return Config{}, err
	}
	if c.RetryBaseDelay, err = durationEnv("TANGO_RETRY_BASE_DELAY", defaultRetryBaseDelay); err != nil {
		return Config{}, err
	}
	if v := strings.TrimSpace(os.Getenv("TANGO_DATE_FORMAT")); v != "" {
		c.DateFormat = v
	}

	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

// Validate chequea lo minimo indispensable para poder hablar con Tango.
func (c Config) Validate() error {
	var missing []string
	if c.TangoBaseURL == "" {
		missing = append(missing, "TANGO_BASE_URL")
	}
	if c.TangoAPIToken == "" {
		missing = append(missing, "TANGO_API_TOKEN")
	}
	if c.TangoCompanyID == "" {
		missing = append(missing, "TANGO_COMPANY_ID")
	}
	if c.TangoSalesProcessID == "" {
		missing = append(missing, "TANGO_SALES_PROCESS_ID")
	}
	if len(missing) > 0 {
		return fmt.Errorf("config: faltan variables obligatorias: %s", strings.Join(missing, ", "))
	}

	u, err := url.Parse(c.TangoBaseURL)
	if err != nil {
		return fmt.Errorf("config: TANGO_BASE_URL invalida: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("config: TANGO_BASE_URL debe ser http:// o https:// (es %q)", u.Scheme)
	}
	if u.Host == "" {
		return errors.New("config: TANGO_BASE_URL sin host")
	}
	if c.HTTPTimeout <= 0 {
		return errors.New("config: TANGO_HTTP_TIMEOUT debe ser > 0")
	}
	if c.MaxRetries < 0 {
		return errors.New("config: TANGO_MAX_RETRIES no puede ser negativo")
	}
	if c.RetryBaseDelay < 0 {
		return errors.New("config: TANGO_RETRY_BASE_DELAY no puede ser negativo")
	}
	if c.DateFormat == "" {
		return errors.New("config: TANGO_DATE_FORMAT vacio")
	}
	return nil
}

// LogValue implementa slog.LogValuer: el token nunca sale por el log.
func (c Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("tango_base_url", c.TangoBaseURL),
		slog.String("tango_company_id", c.TangoCompanyID),
		slog.String("tango_sales_process_id", c.TangoSalesProcessID),
		slog.String("tango_api_token", "[redacted]"),
		slog.Duration("http_timeout", c.HTTPTimeout),
		slog.Int("max_retries", c.MaxRetries),
		slog.Duration("retry_base_delay", c.RetryBaseDelay),
		slog.String("date_format", c.DateFormat),
	)
}

// LoadEnvFile carga pares KEY=VALUE de un archivo al entorno del proceso.
// No pisa variables ya definidas (el entorno real gana sobre el archivo).
// Si el archivo no existe y required es false, no es un error.
func LoadEnvFile(path string, required bool) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) && !required {
			return nil
		}
		return fmt.Errorf("config: no se pudo abrir %s: %w", path, err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	line := 0
	for sc.Scan() {
		line++
		raw := strings.TrimSpace(sc.Text())
		if raw == "" || strings.HasPrefix(raw, "#") {
			continue
		}
		raw = strings.TrimPrefix(raw, "export ")
		k, v, ok := strings.Cut(raw, "=")
		if !ok {
			return fmt.Errorf("config: %s:%d: se esperaba KEY=VALUE", path, line)
		}
		k = strings.TrimSpace(k)
		if k == "" {
			return fmt.Errorf("config: %s:%d: clave vacia", path, line)
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
			v = v[1 : len(v)-1]
		}
		if _, defined := os.LookupEnv(k); defined {
			continue
		}
		if err := os.Setenv(k, v); err != nil {
			return fmt.Errorf("config: no se pudo setear %s: %w", k, err)
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("config: leyendo %s: %w", path, err)
	}
	return nil
}

func durationEnv(key string, def time.Duration) (time.Duration, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("config: %s invalida (%q): usar formato tipo 30s, 2m", key, v)
	}
	return d, nil
}

func intEnv(key string, def int) (int, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("config: %s invalida (%q): se espera un entero", key, v)
	}
	return n, nil
}
