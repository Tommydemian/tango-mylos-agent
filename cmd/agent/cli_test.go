package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const tokenDePrueba = "TOKEN-SECRETO-DE-PRUEBA"

// tangoFalso registra, en orden, el process de cada request que recibe.
type tangoFalso struct {
	*httptest.Server
	mu        sync.Mutex
	processes []string
}

func nuevoTangoFalso(t *testing.T, filasPorProcess map[string][]map[string]any) *tangoFalso {
	t.Helper()
	f := &tangoFalso{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("ApiAuthorization") != tokenDePrueba {
			http.Error(w, "token invalido", http.StatusUnauthorized)
			return
		}
		process := r.URL.Query().Get("process")
		f.mu.Lock()
		f.processes = append(f.processes, process)
		f.mu.Unlock()

		filas := filasPorProcess[process]
		if filas == nil {
			filas = []map[string]any{}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"resultData": map[string]any{
				"list": filas, "pageIndex": 0, "pageSize": len(filas),
				"totalCount": len(filas), "totalPages": 1,
				"hasPreviousPage": false, "hasNextPage": false,
			},
			"message": nil, "exceptionInfo": nil, "succeeded": true,
		})
	}))
	t.Cleanup(f.Server.Close)
	return f
}

func (f *tangoFalso) vistos() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.processes...)
}

// entorno deja la config lista apuntando al Tango falso.
// Devuelve el path a un --env-file inexistente, para que ningun .env del
// disco se cuele en los tests.
func entorno(t *testing.T, baseURL string, extra map[string]string) string {
	t.Helper()
	vars := map[string]string{
		"TANGO_BASE_URL":             baseURL,
		"TANGO_API_TOKEN":            tokenDePrueba,
		"TANGO_COMPANY_ID":           "2",
		"TANGO_SALES_PROCESS_ID":     "17839",
		"TANGO_CUSTOMERS_PROCESS_ID": "17851",
		"TANGO_MAX_RETRIES":          "0",
	}
	for k, v := range extra {
		vars[k] = v
	}
	for k, v := range vars {
		if v == "" {
			os.Unsetenv(k)
			continue
		}
		t.Setenv(k, v)
	}
	return filepath.Join(t.TempDir(), "no-existe.env")
}

// capturar redirige stdout/stderr del comando y devuelve lo impreso.
func capturar(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	prevOut, prevErr := stdout, stderr
	stdout, stderr = &buf, &buf
	t.Cleanup(func() { stdout, stderr = prevOut, prevErr })
	err := fn()
	return buf.String(), err
}

func filasVentas() []map[string]any {
	return []map[string]any{{
		"FECHA_DE_EMISION": "2026-09-19T00:00:00",
		"TIPO_COMPROBANTE": "FAC",
		"NRO_COMPROBANTE":  "B0001500020858",
		"COD_ARTICULO":     "PAT015-029",
		"CANTIDAD":         1,
		"TOTAL":            48760.330579,
	}}
}

func filasClientes() []map[string]any {
	return []map[string]any{{
		"COD_CLIENTE":  "C0001",
		"RAZON_SOCIAL": "SACO NATALIA",
		"COLUMNA_RARA": "no modelada",
	}}
}

func TestSyncSales_UsaElProcessDeVentas(t *testing.T) {
	srv := nuevoTangoFalso(t, map[string][]map[string]any{"17839": filasVentas()})
	envFile := entorno(t, srv.URL, nil)

	out, err := capturar(t, func() error {
		return run([]string{"sync-sales", "--from", "19/09/2026", "--to", "19/09/2026", "--env-file", envFile})
	})
	if err != nil {
		t.Fatalf("sync-sales: %v\n%s", err, out)
	}
	if got := srv.vistos(); len(got) != 1 || got[0] != "17839" {
		t.Errorf("process usados = %v, esperaba [17839]", got)
	}
	if !strings.Contains(out, "Resumen ventas") || !strings.Contains(out, "filas (renglones)  : 1") {
		t.Errorf("resumen inesperado:\n%s", out)
	}
	if strings.Contains(out, "Resumen clientes") {
		t.Errorf("sync-sales no deberia leer clientes:\n%s", out)
	}
}

func TestSyncCustomers_UsaElProcessDeClientes(t *testing.T) {
	srv := nuevoTangoFalso(t, map[string][]map[string]any{"17851": filasClientes()})
	envFile := entorno(t, srv.URL, nil)

	out, err := capturar(t, func() error {
		return run([]string{"sync-customers", "--from", "19/09/2026", "--to", "19/09/2026", "--env-file", envFile})
	})
	if err != nil {
		t.Fatalf("sync-customers: %v\n%s", err, out)
	}
	if got := srv.vistos(); len(got) != 1 || got[0] != "17851" {
		t.Errorf("process usados = %v, esperaba [17851]", got)
	}
	if !strings.Contains(out, "Resumen clientes") || !strings.Contains(out, "filas              : 1") {
		t.Errorf("resumen inesperado:\n%s", out)
	}
}

// sync corre las dos consultas, en secuencia, clientes primero.
func TestSync_ClientesPrimeroDespuesVentas(t *testing.T) {
	srv := nuevoTangoFalso(t, map[string][]map[string]any{
		"17839": filasVentas(),
		"17851": filasClientes(),
	})
	envFile := entorno(t, srv.URL, nil)

	out, err := capturar(t, func() error {
		return run([]string{"sync", "--from", "19/09/2026", "--to", "19/09/2026", "--env-file", envFile})
	})
	if err != nil {
		t.Fatalf("sync: %v\n%s", err, out)
	}

	got := srv.vistos()
	if len(got) != 2 {
		t.Fatalf("esperaba 2 requests, hubo %d: %v", len(got), got)
	}
	if got[0] != "17851" {
		t.Errorf("la primera consulta deberia ser clientes (17851), fue %q", got[0])
	}
	if got[1] != "17839" {
		t.Errorf("la segunda consulta deberia ser ventas (17839), fue %q", got[1])
	}

	// Y el orden tambien se ve en la salida.
	iClientes := strings.Index(out, "Resumen clientes")
	iVentas := strings.Index(out, "Resumen ventas")
	if iClientes < 0 || iVentas < 0 {
		t.Fatalf("faltan resumenes en la salida:\n%s", out)
	}
	if iClientes > iVentas {
		t.Errorf("el resumen de clientes deberia ir primero:\n%s", out)
	}
}

func TestSync_FallaSiFaltaAlgunProcessID(t *testing.T) {
	casos := map[string]struct {
		quitar string
		espera []string
	}{
		"sin clientes": {"TANGO_CUSTOMERS_PROCESS_ID", []string{"TANGO_CUSTOMERS_PROCESS_ID"}},
		"sin ventas":   {"TANGO_SALES_PROCESS_ID", []string{"TANGO_SALES_PROCESS_ID"}},
	}
	for nombre, caso := range casos {
		t.Run(nombre, func(t *testing.T) {
			srv := nuevoTangoFalso(t, nil)
			envFile := entorno(t, srv.URL, map[string]string{caso.quitar: ""})

			out, err := capturar(t, func() error {
				return run([]string{"sync", "--from", "19/09/2026", "--to", "19/09/2026", "--env-file", envFile})
			})
			if err == nil {
				t.Fatalf("esperaba error\n%s", out)
			}
			for _, want := range append(caso.espera, "sync necesita los dos process ids") {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("el error deberia mencionar %q: %v", want, err)
				}
			}
			// Y falla antes de salir a la red.
			if got := srv.vistos(); len(got) != 0 {
				t.Errorf("no deberia haber pedido nada a Tango: %v", got)
			}
		})
	}

	t.Run("faltan los dos", func(t *testing.T) {
		srv := nuevoTangoFalso(t, nil)
		envFile := entorno(t, srv.URL, map[string]string{
			"TANGO_SALES_PROCESS_ID":     "",
			"TANGO_CUSTOMERS_PROCESS_ID": "",
		})
		_, err := capturar(t, func() error {
			return run([]string{"sync", "--from", "19/09/2026", "--to", "19/09/2026", "--env-file", envFile})
		})
		if err == nil {
			t.Fatal("esperaba error")
		}
		// El error nombra las dos que faltan, no solo la primera.
		for _, want := range []string{"TANGO_SALES_PROCESS_ID", "TANGO_CUSTOMERS_PROCESS_ID"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("el error deberia mencionar %q: %v", want, err)
			}
		}
	})
}

// sync-sales no necesita el process de clientes, y viceversa.
func TestComandosIndividualesNoExigenElOtroProcessID(t *testing.T) {
	t.Run("sync-sales sin process de clientes", func(t *testing.T) {
		srv := nuevoTangoFalso(t, map[string][]map[string]any{"17839": filasVentas()})
		envFile := entorno(t, srv.URL, map[string]string{"TANGO_CUSTOMERS_PROCESS_ID": ""})
		if out, err := capturar(t, func() error {
			return run([]string{"sync-sales", "--from", "19/09/2026", "--to", "19/09/2026", "--env-file", envFile})
		}); err != nil {
			t.Fatalf("no deberia fallar: %v\n%s", err, out)
		}
	})
	t.Run("sync-customers sin process de ventas", func(t *testing.T) {
		srv := nuevoTangoFalso(t, map[string][]map[string]any{"17851": filasClientes()})
		envFile := entorno(t, srv.URL, map[string]string{"TANGO_SALES_PROCESS_ID": ""})
		if out, err := capturar(t, func() error {
			return run([]string{"sync-customers", "--from", "19/09/2026", "--to", "19/09/2026", "--env-file", envFile})
		}); err != nil {
			t.Fatalf("no deberia fallar: %v\n%s", err, out)
		}
	})
}

// Ningun secreto en la salida, ni siquiera con --log-level debug, que es el
// nivel que imprime cada URL.
func TestNingunSecretoEnLaSalida(t *testing.T) {
	srv := nuevoTangoFalso(t, map[string][]map[string]any{
		"17839": filasVentas(),
		"17851": filasClientes(),
	})
	envFile := entorno(t, srv.URL, nil)

	out, err := capturar(t, func() error {
		return run([]string{"sync", "--from", "19/09/2026", "--to", "19/09/2026",
			"--env-file", envFile, "--log-level", "debug"})
	})
	if err != nil {
		t.Fatalf("sync: %v\n%s", err, out)
	}
	if strings.Contains(out, tokenDePrueba) {
		t.Fatalf("el token aparecio en la salida:\n%s", out)
	}
	if !strings.Contains(out, "[redacted]") {
		t.Errorf("con debug la config se loguea con el token enmascarado:\n%s", out)
	}
	// Chequeo de que el test sirve: debug realmente imprimio las URLs.
	if !strings.Contains(out, "GetApiLiveQueryData") {
		t.Errorf("el log debug no imprimio los requests:\n%s", out)
	}
}

func TestSync_EscribeLosDosJSONL(t *testing.T) {
	srv := nuevoTangoFalso(t, map[string][]map[string]any{
		"17839": filasVentas(),
		"17851": filasClientes(),
	})
	envFile := entorno(t, srv.URL, nil)
	dir := t.TempDir()

	out, err := capturar(t, func() error {
		return run([]string{"sync", "--from", "19/09/2026", "--to", "19/09/2026",
			"--env-file", envFile, "--out-dir", dir})
	})
	if err != nil {
		t.Fatalf("sync: %v\n%s", err, out)
	}

	for archivo, campo := range map[string]string{
		"clientes.jsonl": "COLUMNA_RARA",
		"ventas.jsonl":   "NRO_COMPROBANTE",
	} {
		b, err := os.ReadFile(filepath.Join(dir, archivo))
		if err != nil {
			t.Fatalf("%s: %v", archivo, err)
		}
		if !strings.Contains(string(b), campo) {
			t.Errorf("%s no tiene %s:\n%s", archivo, campo, b)
		}
		var m map[string]any
		if err := json.Unmarshal(bytes.TrimSpace(b), &m); err != nil {
			t.Errorf("%s no es JSONL valido: %v", archivo, err)
		}
	}
}

// Sin --out/--out-dir no se escribe nada a disco: el volcado es opt-in.
func TestSalidaJSONLEsOptIn(t *testing.T) {
	srv := nuevoTangoFalso(t, map[string][]map[string]any{
		"17839": filasVentas(),
		"17851": filasClientes(),
	})
	envFile := entorno(t, srv.URL, nil)
	dir := t.TempDir()
	t.Chdir(dir) // se restaura solo al terminar el test

	if _, err := capturar(t, func() error {
		return run([]string{"sync", "--from", "19/09/2026", "--to", "19/09/2026", "--env-file", envFile})
	}); err != nil {
		t.Fatalf("sync: %v", err)
	}

	entradas, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entradas) != 0 {
		var nombres []string
		for _, e := range entradas {
			nombres = append(nombres, e.Name())
		}
		t.Errorf("no deberia haber escrito archivos: %v", nombres)
	}
}
