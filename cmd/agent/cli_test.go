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

// pedido es un request que recibio el Tango falso.
type pedido struct {
	process     string
	customQuery string
	tenia       bool // si el param customQuery venia en la URL
}

// tangoFalso registra, en orden, cada request que recibe.
type tangoFalso struct {
	*httptest.Server
	mu      sync.Mutex
	pedidos []pedido
	fallaEn string // process que responde 500 en vez de datos
}

func nuevoTangoFalso(t *testing.T, filasPorProcess map[string][]map[string]any) *tangoFalso {
	t.Helper()
	f := &tangoFalso{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("ApiAuthorization") != tokenDePrueba {
			http.Error(w, "token invalido", http.StatusUnauthorized)
			return
		}
		q := r.URL.Query()
		process := q.Get("process")
		cq, tenia := q["customQuery"]
		p := pedido{process: process, tenia: tenia}
		if tenia {
			p.customQuery = cq[0]
		}
		f.mu.Lock()
		f.pedidos = append(f.pedidos, p)
		falla := f.fallaEn
		f.mu.Unlock()

		if falla != "" && falla == process {
			http.Error(w, "la consulta exploto", http.StatusInternalServerError)
			return
		}

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

// vistos devuelve los process pedidos, en orden.
func (f *tangoFalso) vistos() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, p := range f.pedidos {
		out = append(out, p.process)
	}
	return out
}

// pedidoDe devuelve el primer request hecho a ese process.
func (f *tangoFalso) pedidoDe(t *testing.T, process string) pedido {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.pedidos {
		if p.process == process {
			return p
		}
	}
	t.Fatalf("no hubo ningun request al process %s (hubo %d)", process, len(f.pedidos))
	return pedido{}
}

// entorno deja la config lista apuntando al Tango falso.
// Devuelve el path a un --env-file inexistente, para que ningun .env del
// disco se cuele en los tests.
func entorno(t *testing.T, baseURL string, extra map[string]string) string {
	t.Helper()
	vars := map[string]string{
		"TANGO_BASE_URL":                  baseURL,
		"TANGO_API_TOKEN":                 tokenDePrueba,
		"TANGO_COMPANY_ID":                "2",
		"TANGO_SALES_PROCESS_ID":          "17839",
		"TANGO_SALES_CUSTOM_QUERY_ID":     "9",
		"TANGO_CUSTOMERS_PROCESS_ID":      "17851",
		"TANGO_CUSTOMERS_CUSTOM_QUERY_ID": "10",
		"TANGO_MAX_RETRIES":               "0",
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

// filasVentas imita lo que devuelve la custom query 9: los campos de siempre
// mas los que agrega el schema guardado.
func filasVentas() []map[string]any {
	return []map[string]any{{
		"FECHA_DE_EMISION": "2026-09-19T00:00:00",
		"TIPO_COMPROBANTE": "FAC",
		"NRO_COMPROBANTE":  "B0001500020858",
		"NOMBRE_VENDEDOR":  "GABRIELA CONTARTESE",
		"RAZON_SOCIAL":     "SACO NATALIA",
		"COD_ARTICULO":     "PAT015-029",
		"DESCRIPCION":      "TEXTIL VERBENA 1,5L",
		"CANTIDAD":         1,
		"TOTAL":            48760.330579,
		// Extras de la custom query 9, que el struct no modela.
		"COD_CLIENTE":           "C0001",
		"DESCRIPCION_ADICIONAL": "envio a domicilio",
	}}
}

// filasClientes imita la custom query 10.
func filasClientes() []map[string]any {
	return []map[string]any{{
		"COD_CLIENTE":       "C0001",
		"RAZON_SOCIAL":      "SACO NATALIA",
		"TIPO_DE_DOCUMENTO": "CUIT",
		"NUMERO":            "20-12345678-9",
		"DOMICILIO":         "ARENALES 1234",
		"LOCALIDAD":         "CABA",
		"EMAIL":             "cliente@ejemplo.com",
		"CONDICION_DE_IVA":  "RI",
		"DESC_RUBRO":        "PERFUMERIA",
		"COLUMNA_RARA":      "no modelada",
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

// --- custom query: precedencia y transporte ---------------------------------

// El customQuery de cada dataset sale de su variable de entorno.
func TestCustomQueryDesdeElEntorno(t *testing.T) {
	casos := []struct {
		comando  string
		process  string
		esperado string
	}{
		{"sync-sales", "17839", "9"},
		{"sync-customers", "17851", "10"},
	}
	for _, caso := range casos {
		t.Run(caso.comando, func(t *testing.T) {
			srv := nuevoTangoFalso(t, map[string][]map[string]any{
				"17839": filasVentas(), "17851": filasClientes(),
			})
			envFile := entorno(t, srv.URL, nil)

			if out, err := capturar(t, func() error {
				return run([]string{caso.comando, "--from", "22/09/2026", "--to", "23/09/2026", "--env-file", envFile})
			}); err != nil {
				t.Fatalf("%s: %v\n%s", caso.comando, err, out)
			}

			p := srv.pedidoDe(t, caso.process)
			if !p.tenia {
				t.Fatalf("no se mando el param customQuery")
			}
			if p.customQuery != caso.esperado {
				t.Errorf("customQuery = %q, esperaba %q", p.customQuery, caso.esperado)
			}
		})
	}
}

// --custom-query pisa lo que diga el entorno.
func TestCustomQueryFlagPisaElEntorno(t *testing.T) {
	srv := nuevoTangoFalso(t, map[string][]map[string]any{
		"17839": filasVentas(), "17851": filasClientes(),
	})
	envFile := entorno(t, srv.URL, nil) // env trae 9 y 10

	if out, err := capturar(t, func() error {
		return run([]string{"sync-sales", "--from", "22/09/2026", "--to", "23/09/2026",
			"--env-file", envFile, "--custom-query", "99"})
	}); err != nil {
		t.Fatalf("sync-sales: %v\n%s", err, out)
	}
	if p := srv.pedidoDe(t, "17839"); p.customQuery != "99" {
		t.Errorf("customQuery = %q, el flag deberia ganarle al entorno (9)", p.customQuery)
	}
}

// En sync, el override manual aplica a las dos etapas.
func TestCustomQueryFlagEnSyncAplicaALasDos(t *testing.T) {
	srv := nuevoTangoFalso(t, map[string][]map[string]any{
		"17839": filasVentas(), "17851": filasClientes(),
	})
	envFile := entorno(t, srv.URL, nil)

	if out, err := capturar(t, func() error {
		return run([]string{"sync", "--from", "22/09/2026", "--to", "23/09/2026",
			"--env-file", envFile, "--custom-query", "77"})
	}); err != nil {
		t.Fatalf("sync: %v\n%s", err, out)
	}
	for _, process := range []string{"17851", "17839"} {
		if p := srv.pedidoDe(t, process); p.customQuery != "77" {
			t.Errorf("process %s: customQuery = %q, esperaba 77", process, p.customQuery)
		}
	}
}

// Sin custom query configurado ni pasado, el param no viaja y la consulta
// corre igual con su schema por defecto.
func TestCustomQueryVacioSigueSiendoValido(t *testing.T) {
	srv := nuevoTangoFalso(t, map[string][]map[string]any{"17839": filasVentas()})
	envFile := entorno(t, srv.URL, map[string]string{"TANGO_SALES_CUSTOM_QUERY_ID": ""})

	out, err := capturar(t, func() error {
		return run([]string{"sync-sales", "--from", "22/09/2026", "--to", "23/09/2026", "--env-file", envFile})
	})
	if err != nil {
		t.Fatalf("sync-sales sin custom query deberia funcionar: %v\n%s", err, out)
	}
	if p := srv.pedidoDe(t, "17839"); p.tenia {
		t.Errorf("no deberia viajar el param customQuery, viajo %q", p.customQuery)
	}
	if !strings.Contains(out, "filas (renglones)  : 1") {
		t.Errorf("deberia haber leido la fila igual:\n%s", out)
	}
}

// --custom-query "" explicito apaga el del entorno.
func TestCustomQueryFlagVacioApagaElDelEntorno(t *testing.T) {
	srv := nuevoTangoFalso(t, map[string][]map[string]any{"17839": filasVentas()})
	envFile := entorno(t, srv.URL, nil) // env trae 9

	if out, err := capturar(t, func() error {
		return run([]string{"sync-sales", "--from", "22/09/2026", "--to", "23/09/2026",
			"--env-file", envFile, "--custom-query", ""})
	}); err != nil {
		t.Fatalf("sync-sales: %v\n%s", err, out)
	}
	if p := srv.pedidoDe(t, "17839"); p.tenia {
		t.Errorf("--custom-query \"\" deberia apagar el del entorno, viajo %q", p.customQuery)
	}
}

// --- sync: no seguir de largo si clientes falla -----------------------------

func TestSync_NoSigueSiClientesFalla(t *testing.T) {
	srv := nuevoTangoFalso(t, map[string][]map[string]any{
		"17839": filasVentas(), "17851": filasClientes(),
	})
	srv.fallaEn = "17851" // la consulta de clientes responde 500

	envFile := entorno(t, srv.URL, nil)
	out, err := capturar(t, func() error {
		return run([]string{"sync", "--from", "22/09/2026", "--to", "23/09/2026", "--env-file", envFile})
	})
	if err == nil {
		t.Fatalf("sync deberia fallar\n%s", out)
	}
	for _, want := range []string{"fallo la etapa de clientes", "no se corrieron las ventas"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("el error deberia decir %q: %v", want, err)
		}
	}
	// Lo importante: las ventas no se leyeron.
	for _, process := range srv.vistos() {
		if process == "17839" {
			t.Fatalf("no deberia haber consultado ventas: %v", srv.vistos())
		}
	}
	if strings.Contains(out, "Resumen ventas") {
		t.Errorf("no deberia imprimir resumen de ventas:\n%s", out)
	}
}

func TestSync_FallaSiFallanLasVentas(t *testing.T) {
	srv := nuevoTangoFalso(t, map[string][]map[string]any{
		"17839": filasVentas(), "17851": filasClientes(),
	})
	srv.fallaEn = "17839"

	envFile := entorno(t, srv.URL, nil)
	out, err := capturar(t, func() error {
		return run([]string{"sync", "--from", "22/09/2026", "--to", "23/09/2026", "--env-file", envFile})
	})
	if err == nil {
		t.Fatalf("sync deberia fallar\n%s", out)
	}
	if !strings.Contains(err.Error(), "fallo la etapa de ventas") {
		t.Errorf("el error deberia distinguir la etapa: %v", err)
	}
	// Los clientes si se leyeron, y el resumen lo muestra.
	if !strings.Contains(out, "Resumen clientes") {
		t.Errorf("deberia haber resumen de clientes:\n%s", out)
	}
}

// --- raw JSON: nada se pierde ----------------------------------------------

// Las columnas que agrega la custom query 9 llegan intactas al JSONL aunque
// SalesLine no las modele, y las que si modela siguen funcionando.
func TestVentas_ConservaColumnasDeLaCustomQuery(t *testing.T) {
	srv := nuevoTangoFalso(t, map[string][]map[string]any{"17839": filasVentas()})
	envFile := entorno(t, srv.URL, nil)
	archivo := filepath.Join(t.TempDir(), "ventas.jsonl")

	out, err := capturar(t, func() error {
		return run([]string{"sync-sales", "--from", "22/09/2026", "--to", "23/09/2026",
			"--env-file", envFile, "--out", archivo})
	})
	if err != nil {
		t.Fatalf("sync-sales: %v\n%s", err, out)
	}

	b, err := os.ReadFile(archivo)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(b), &m); err != nil {
		t.Fatalf("JSONL invalido: %v", err)
	}
	// Extras de la custom query.
	if m["COD_CLIENTE"] != "C0001" || m["DESCRIPCION_ADICIONAL"] != "envio a domicilio" {
		t.Errorf("se perdieron columnas de la custom query: %v", m)
	}
	// Y los campos de siempre siguen enteros.
	for k, want := range map[string]any{
		"FECHA_DE_EMISION": "2026-09-19T00:00:00",
		"TIPO_COMPROBANTE": "FAC",
		"NRO_COMPROBANTE":  "B0001500020858",
		"NOMBRE_VENDEDOR":  "GABRIELA CONTARTESE",
		"RAZON_SOCIAL":     "SACO NATALIA",
		"COD_ARTICULO":     "PAT015-029",
		"DESCRIPCION":      "TEXTIL VERBENA 1,5L",
	} {
		if m[k] != want {
			t.Errorf("%s = %v, esperaba %v", k, m[k], want)
		}
	}
	if m["CANTIDAD"] != float64(1) || m["TOTAL"] != 48760.330579 {
		t.Errorf("numeros mal preservados: %v / %v", m["CANTIDAD"], m["TOTAL"])
	}
	// El resumen sigue leyendo bien los campos que si modela.
	if !strings.Contains(out, "FAC=1") || !strings.Contains(out, "comprobantes       : 1") {
		t.Errorf("el resumen no parseo los campos modelados:\n%s", out)
	}
}

func TestClientes_ConservaTodasLasColumnas(t *testing.T) {
	srv := nuevoTangoFalso(t, map[string][]map[string]any{"17851": filasClientes()})
	envFile := entorno(t, srv.URL, nil)
	archivo := filepath.Join(t.TempDir(), "clientes.jsonl")

	if out, err := capturar(t, func() error {
		return run([]string{"sync-customers", "--from", "22/09/2026", "--to", "23/09/2026",
			"--env-file", envFile, "--out", archivo})
	}); err != nil {
		t.Fatalf("sync-customers: %v\n%s", err, out)
	}

	b, err := os.ReadFile(archivo)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(b), &m); err != nil {
		t.Fatalf("JSONL invalido: %v", err)
	}
	esperadas := filasClientes()[0]
	if len(m) != len(esperadas) {
		t.Errorf("llegaron %d columnas de %d: %v", len(m), len(esperadas), m)
	}
	for k := range esperadas {
		if _, ok := m[k]; !ok {
			t.Errorf("falta la columna %s en el JSONL", k)
		}
	}
}
