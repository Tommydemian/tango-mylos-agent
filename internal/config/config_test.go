package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func setEnv(t *testing.T, kv map[string]string) {
	t.Helper()
	for _, k := range []string{
		"TANGO_BASE_URL", "TANGO_API_TOKEN", "TANGO_COMPANY_ID", "TANGO_SALES_PROCESS_ID",
		"TANGO_HTTP_TIMEOUT", "TANGO_MAX_RETRIES", "TANGO_RETRY_BASE_DELAY", "TANGO_DATE_FORMAT",
	} {
		os.Unsetenv(k)
	}
	for k, v := range kv {
		t.Setenv(k, v)
	}
}

func validEnv() map[string]string {
	return map[string]string{
		"TANGO_BASE_URL":         "http://arenales_tango:17000",
		"TANGO_API_TOKEN":        "secreto",
		"TANGO_COMPANY_ID":       "2",
		"TANGO_SALES_PROCESS_ID": "17839",
	}
}

func TestLoad_ConDefaults(t *testing.T) {
	setEnv(t, validEnv())
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.TangoBaseURL != "http://arenales_tango:17000" || c.TangoCompanyID != "2" || c.TangoSalesProcessID != "17839" {
		t.Errorf("config = %+v", c)
	}
	if c.HTTPTimeout != 60*time.Second || c.MaxRetries != 3 || c.RetryBaseDelay != 500*time.Millisecond {
		t.Errorf("defaults = %v %v %v", c.HTTPTimeout, c.MaxRetries, c.RetryBaseDelay)
	}
	if c.DateFormat != "02/01/2006" {
		t.Errorf("date format = %q, el default (hipotesis) es dd/MM/yyyy", c.DateFormat)
	}
}

func TestLoad_FaltanObligatorias(t *testing.T) {
	setEnv(t, map[string]string{"TANGO_BASE_URL": "http://x:17000"})
	_, err := Load()
	if err == nil {
		t.Fatal("esperaba error")
	}
	for _, want := range []string{"TANGO_API_TOKEN", "TANGO_COMPANY_ID", "TANGO_SALES_PROCESS_ID"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("el error deberia nombrar %s: %v", want, err)
		}
	}
}

func TestLoad_BaseURLInvalida(t *testing.T) {
	env := validEnv()
	env["TANGO_BASE_URL"] = "arenales_tango:17000"
	setEnv(t, env)
	if _, err := Load(); err == nil {
		t.Fatal("esperaba error: falta el esquema http://")
	}
}

func TestLoad_OverridesOpcionales(t *testing.T) {
	env := validEnv()
	env["TANGO_HTTP_TIMEOUT"] = "5s"
	env["TANGO_MAX_RETRIES"] = "7"
	env["TANGO_RETRY_BASE_DELAY"] = "10ms"
	env["TANGO_DATE_FORMAT"] = "2006-01-02"
	setEnv(t, env)
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.HTTPTimeout != 5*time.Second || c.MaxRetries != 7 || c.RetryBaseDelay != 10*time.Millisecond {
		t.Errorf("config = %+v", c)
	}
	if c.DateFormat != "2006-01-02" {
		t.Errorf("date format = %q", c.DateFormat)
	}
}

func TestLoad_OpcionalesInvalidas(t *testing.T) {
	env := validEnv()
	env["TANGO_MAX_RETRIES"] = "muchos"
	setEnv(t, env)
	if _, err := Load(); err == nil {
		t.Fatal("esperaba error")
	}
}

func TestLogValue_NoExponeElToken(t *testing.T) {
	c := Config{
		TangoBaseURL:        "http://x:17000",
		TangoAPIToken:       "TOKEN-SECRETO",
		TangoCompanyID:      "2",
		TangoSalesProcessID: "17839",
	}
	if s := c.LogValue().String(); strings.Contains(s, "TOKEN-SECRETO") {
		t.Fatalf("LogValue filtro el token: %s", s)
	}
}

func TestLoadEnvFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	body := "# comentario\n\nTANGO_BASE_URL=http://arenales_tango:17000\n" +
		"export TANGO_API_TOKEN=\"desde-archivo\"\n" +
		"TANGO_COMPANY_ID='2'\nTANGO_SALES_PROCESS_ID=17839\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	setEnv(t, map[string]string{"TANGO_API_TOKEN": "desde-entorno"})
	if err := LoadEnvFile(path, true); err != nil {
		t.Fatalf("LoadEnvFile: %v", err)
	}
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.TangoBaseURL != "http://arenales_tango:17000" || c.TangoCompanyID != "2" {
		t.Errorf("no cargo del archivo: %+v", c)
	}
	if c.TangoAPIToken != "desde-entorno" {
		t.Errorf("el entorno real debe ganar sobre el archivo, token = %q", c.TangoAPIToken)
	}
}

func TestLoadEnvFile_InexistenteNoEsErrorSiNoEsRequerido(t *testing.T) {
	path := filepath.Join(t.TempDir(), "no-existe")
	if err := LoadEnvFile(path, false); err != nil {
		t.Errorf("no deberia fallar: %v", err)
	}
	if err := LoadEnvFile(path, true); err == nil {
		t.Error("con required=true deberia fallar")
	}
}

func TestLoadEnvFile_LineaInvalida(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("esto no es un par\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := LoadEnvFile(path, true); err == nil {
		t.Error("esperaba error de parseo")
	}
}
