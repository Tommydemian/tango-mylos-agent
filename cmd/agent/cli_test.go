package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

const (
	tokenDePrueba = "TOKEN-SECRETO-DE-PRUEBA"
	tokenMylos    = "MYLOS-TOKEN-SECRETO-DE-PRUEBA"

	pathClientes = "/integrations/tango/customers/batch"
	pathVentas   = "/integrations/tango/sales/batch"
)

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
// nuevoTangoFalsoPaginado respeta pageSize/pageIndex, para poder verificar
// que se manda un batch por pagina.
func nuevoTangoFalsoPaginado(t *testing.T, filasPorProcess map[string][]map[string]any) *tangoFalso {
	t.Helper()
	f := &tangoFalso{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("ApiAuthorization") != tokenDePrueba {
			http.Error(w, "token invalido", http.StatusUnauthorized)
			return
		}
		q := r.URL.Query()
		process := q.Get("process")
		f.mu.Lock()
		f.pedidos = append(f.pedidos, pedido{process: process})
		f.mu.Unlock()

		size, _ := strconv.Atoi(q.Get("pageSize"))
		if size <= 0 {
			size = 1
		}
		idx, _ := strconv.Atoi(q.Get("pageIndex"))

		todas := filasPorProcess[process]
		total := len(todas)
		pages := (total + size - 1) / size
		if pages == 0 {
			pages = 1
		}
		desde := idx * size
		hasta := min(desde+size, total)
		chunk := []map[string]any{}
		if desde < total {
			chunk = todas[desde:hasta]
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"resultData": map[string]any{
				"list": chunk, "pageIndex": idx, "pageSize": size,
				"totalCount": total, "totalPages": pages,
				"hasPreviousPage": idx > 0, "hasNextPage": idx < pages-1,
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

// ingesta es un request recibido por el MYLOS falso.
type ingesta struct {
	path  string
	auth  string
	batch map[string]any
}

// mylosFalso registra cada batch recibido y puede responder lo que se le pida.
type mylosFalso struct {
	*httptest.Server
	mu          sync.Mutex
	ingestas    []ingesta
	duplicate   bool   // responder duplicate=true / stored=false
	fallaEn     string // path que responde con status fallaStatus
	fallaStatus int
}

func nuevoMylosFalso(t *testing.T) *mylosFalso {
	t.Helper()
	m := &mylosFalso{fallaStatus: http.StatusInternalServerError}
	m.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)

		m.mu.Lock()
		m.ingestas = append(m.ingestas, ingesta{path: r.URL.Path, auth: r.Header.Get("Authorization"), batch: b})
		falla, status, dup := m.fallaEn, m.fallaStatus, m.duplicate
		m.mu.Unlock()

		if falla != "" && falla == r.URL.Path {
			http.Error(w, "la ingesta fallo", status)
			return
		}
		rows, _ := b["rows"].([]any)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"batch_id": "batch-abc-123", "received": len(rows),
			"stored": !dup, "duplicate": dup,
		})
	}))
	t.Cleanup(m.Server.Close)
	return m
}

func (m *mylosFalso) recibidas() []ingesta {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]ingesta(nil), m.ingestas...)
}

func (m *mylosFalso) primeraA(t *testing.T, path string) ingesta {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, i := range m.ingestas {
		if i.path == path {
			return i
		}
	}
	t.Fatalf("no hubo ningun POST a %s (hubo %d)", path, len(m.ingestas))
	return ingesta{}
}

// entorno deja la config lista apuntando al Tango falso.
// Devuelve el path a un --env-file inexistente, para que ningun .env del
// disco se cuele en los tests.
// Si extra no trae MYLOS_BASE_URL, se levanta un MYLOS falso que acepta todo:
// asi los tests que no miran la ingesta no tienen que armarla.
func entorno(t *testing.T, tangoURL string, extra map[string]string) string {
	t.Helper()
	mylosURL := extra["MYLOS_BASE_URL"]
	if mylosURL == "" {
		mylosURL = nuevoMylosFalso(t).URL
	}
	vars := map[string]string{
		"TANGO_BASE_URL":                  tangoURL,
		"MYLOS_BASE_URL":                  mylosURL,
		"MYLOS_INGEST_TOKEN":              tokenMylos,
		"MYLOS_MAX_RETRIES":               "0",
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

// --- envio a MYLOS ----------------------------------------------------------

func conMylos(t *testing.T, my *mylosFalso, extra map[string]string) map[string]string {
	t.Helper()
	out := map[string]string{"MYLOS_BASE_URL": my.URL}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

// Cada dataset postea a su endpoint, con el Bearer correcto y la metadata
// que espera el backend.
func TestPOST_EndpointMetadataYAuth(t *testing.T) {
	casos := []struct {
		comando       string
		path          string
		processID     float64
		customQueryID string
	}{
		{"sync-customers", pathClientes, 17851, "10"},
		{"sync-sales", pathVentas, 17839, "9"},
	}
	for _, caso := range casos {
		t.Run(caso.comando, func(t *testing.T) {
			srv := nuevoTangoFalso(t, map[string][]map[string]any{
				"17839": filasVentas(), "17851": filasClientes(),
			})
			my := nuevoMylosFalso(t)
			envFile := entorno(t, srv.URL, conMylos(t, my, nil))

			out, err := capturar(t, func() error {
				return run([]string{caso.comando, "--from", "22/09/2026", "--to", "23/09/2026", "--env-file", envFile})
			})
			if err != nil {
				t.Fatalf("%s: %v\n%s", caso.comando, err, out)
			}

			in := my.primeraA(t, caso.path)
			if in.auth != "Bearer "+tokenMylos {
				t.Errorf("Authorization = %q", in.auth)
			}
			if got := in.batch["company_id"]; got != float64(2) {
				t.Errorf("company_id = %v (%T), esperaba 2", got, got)
			}
			if got := in.batch["process_id"]; got != caso.processID {
				t.Errorf("process_id = %v, esperaba %v", got, caso.processID)
			}
			if got := in.batch["custom_query_id"]; got != caso.customQueryID {
				t.Errorf("custom_query_id = %v, esperaba %q", got, caso.customQueryID)
			}
			if in.batch["from_date"] != "22/09/2026" || in.batch["to_date"] != "23/09/2026" {
				t.Errorf("fechas = %v -> %v", in.batch["from_date"], in.batch["to_date"])
			}
			// Solo un POST: el otro dataset no se toca.
			if n := len(my.recibidas()); n != 1 {
				t.Errorf("esperaba 1 POST, hubo %d", n)
			}
			if !strings.Contains(out, "batch-abc-123") {
				t.Errorf("el resumen deberia mostrar el batch_id:\n%s", out)
			}
		})
	}
}

// Las filas llegan a MYLOS tal cual salieron de Tango, columnas raras incluidas.
func TestPOST_ConservaRowsCrudas(t *testing.T) {
	srv := nuevoTangoFalso(t, map[string][]map[string]any{
		"17839": filasVentas(), "17851": filasClientes(),
	})
	my := nuevoMylosFalso(t)
	envFile := entorno(t, srv.URL, conMylos(t, my, nil))

	if out, err := capturar(t, func() error {
		return run([]string{"sync", "--from", "22/09/2026", "--to", "23/09/2026", "--env-file", envFile})
	}); err != nil {
		t.Fatalf("sync: %v\n%s", err, out)
	}

	esperado := map[string]map[string]any{
		pathClientes: filasClientes()[0],
		pathVentas:   filasVentas()[0],
	}
	for path, fila := range esperado {
		in := my.primeraA(t, path)
		rows, ok := in.batch["rows"].([]any)
		if !ok || len(rows) != 1 {
			t.Fatalf("%s: rows = %v", path, in.batch["rows"])
		}
		row, ok := rows[0].(map[string]any)
		if !ok {
			t.Fatalf("%s: la fila no es un objeto: %v", path, rows[0])
		}
		if len(row) != len(fila) {
			t.Errorf("%s: llegaron %d columnas de %d: %v", path, len(row), len(fila), row)
		}
		for k, v := range fila {
			if fmt.Sprint(row[k]) != fmt.Sprint(v) {
				t.Errorf("%s: columna %s = %v, esperaba %v", path, k, row[k], v)
			}
		}
	}
}

// duplicate=true es exito: el comando termina bien y sync sigue con ventas.
func TestPOST_DuplicateEsExitoYSyncSigue(t *testing.T) {
	srv := nuevoTangoFalso(t, map[string][]map[string]any{
		"17839": filasVentas(), "17851": filasClientes(),
	})
	my := nuevoMylosFalso(t)
	my.duplicate = true
	envFile := entorno(t, srv.URL, conMylos(t, my, nil))

	out, err := capturar(t, func() error {
		return run([]string{"sync", "--from", "22/09/2026", "--to", "23/09/2026", "--env-file", envFile})
	})
	if err != nil {
		t.Fatalf("duplicate no deberia ser error: %v\n%s", err, out)
	}

	var paths []string
	for _, in := range my.recibidas() {
		paths = append(paths, in.path)
	}
	if len(paths) != 2 || paths[0] != pathClientes || paths[1] != pathVentas {
		t.Errorf("con duplicate sync tiene que completar las dos etapas: %v", paths)
	}
	if !strings.Contains(out, "stored / duplicate: 0 / 1") {
		t.Errorf("el resumen deberia contar el duplicado:\n%s", out)
	}
}

// Si el POST de clientes falla, no se leen ni se envian las ventas.
func TestPOST_SyncNoSigueSiFallaElPOSTDeClientes(t *testing.T) {
	srv := nuevoTangoFalso(t, map[string][]map[string]any{
		"17839": filasVentas(), "17851": filasClientes(),
	})
	my := nuevoMylosFalso(t)
	my.fallaEn = pathClientes
	my.fallaStatus = http.StatusUnprocessableEntity
	envFile := entorno(t, srv.URL, conMylos(t, my, nil))

	out, err := capturar(t, func() error {
		return run([]string{"sync", "--from", "22/09/2026", "--to", "23/09/2026", "--env-file", envFile})
	})
	if err == nil {
		t.Fatalf("esperaba error\n%s", out)
	}
	if !strings.Contains(err.Error(), "fallo la etapa de clientes") {
		t.Errorf("el error deberia identificar la etapa: %v", err)
	}
	for _, in := range my.recibidas() {
		if in.path == pathVentas {
			t.Fatal("no deberia haber posteado ventas")
		}
	}
	for _, p := range srv.vistos() {
		if p == "17839" {
			t.Fatal("no deberia haber leido ventas de Tango")
		}
	}
}

// 422 no se reintenta; 500 si. Se verifica contando los POST que llegaron.
func TestPOST_PoliticaDeReintentos(t *testing.T) {
	casos := map[string]struct {
		status    int
		reintenta bool
	}{
		"401": {http.StatusUnauthorized, false},
		"422": {http.StatusUnprocessableEntity, false},
		"429": {http.StatusTooManyRequests, true},
		"500": {http.StatusInternalServerError, true},
	}
	for nombre, caso := range casos {
		t.Run(nombre, func(t *testing.T) {
			srv := nuevoTangoFalso(t, map[string][]map[string]any{"17851": filasClientes()})
			my := nuevoMylosFalso(t)
			my.fallaEn = pathClientes
			my.fallaStatus = caso.status
			envFile := entorno(t, srv.URL, conMylos(t, my, map[string]string{
				"MYLOS_MAX_RETRIES":      "2",
				"MYLOS_RETRY_BASE_DELAY": "1ms",
			}))

			if _, err := capturar(t, func() error {
				return run([]string{"sync-customers", "--from", "22/09/2026", "--to", "23/09/2026", "--env-file", envFile})
			}); err == nil {
				t.Fatal("esperaba error")
			}

			n := len(my.recibidas())
			if caso.reintenta && n != 3 {
				t.Errorf("%s deberia reintentarse (1 + 2), hubo %d POST", nombre, n)
			}
			if !caso.reintenta && n != 1 {
				t.Errorf("%s no deberia reintentarse, hubo %d POST", nombre, n)
			}
		})
	}
}

// El token de ingesta no aparece en la salida, ni con --log-level debug.
func TestPOST_NingunSecretoEnLaSalida(t *testing.T) {
	srv := nuevoTangoFalso(t, map[string][]map[string]any{
		"17839": filasVentas(), "17851": filasClientes(),
	})
	my := nuevoMylosFalso(t)
	envFile := entorno(t, srv.URL, conMylos(t, my, nil))

	out, err := capturar(t, func() error {
		return run([]string{"sync", "--from", "22/09/2026", "--to", "23/09/2026",
			"--env-file", envFile, "--log-level", "debug"})
	})
	if err != nil {
		t.Fatalf("sync: %v\n%s", err, out)
	}
	for _, secreto := range []string{tokenMylos, tokenDePrueba} {
		if strings.Contains(out, secreto) {
			t.Fatalf("un secreto aparecio en la salida:\n%s", out)
		}
	}
	// El destino si se loguea, sin credenciales.
	if !strings.Contains(out, pathClientes) || !strings.Contains(out, pathVentas) {
		t.Errorf("deberia loguear los endpoints destino:\n%s", out)
	}
}

// Aunque MYLOS devuelva el token en un body de error, no llega al log.
func TestPOST_TokenDevueltoPorElBackendSeRedacta(t *testing.T) {
	srv := nuevoTangoFalso(t, map[string][]map[string]any{"17851": filasClientes()})
	my := nuevoMylosFalso(t)
	my.Server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "token invalido: "+strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "),
			http.StatusUnauthorized)
	})
	envFile := entorno(t, srv.URL, conMylos(t, my, nil))

	out, err := capturar(t, func() error {
		return run([]string{"sync-customers", "--from", "22/09/2026", "--to", "23/09/2026", "--env-file", envFile})
	})
	if err == nil {
		t.Fatal("esperaba error")
	}
	if strings.Contains(err.Error(), tokenMylos) || strings.Contains(out, tokenMylos) {
		t.Fatalf("el token se filtro.\nerror: %v\nsalida: %s", err, out)
	}
	if !strings.Contains(err.Error(), "[redacted]") {
		t.Errorf("el body deberia venir enmascarado: %v", err)
	}
}

// Sin config de MYLOS, los comandos fallan claro y sin leer nada de Tango.
func TestPOST_FallaSiFaltaLaConfigDeMylos(t *testing.T) {
	for _, falta := range []string{"MYLOS_BASE_URL", "MYLOS_INGEST_TOKEN"} {
		for _, comando := range []string{"sync-customers", "sync-sales", "sync"} {
			t.Run(falta+"/"+comando, func(t *testing.T) {
				srv := nuevoTangoFalso(t, map[string][]map[string]any{
					"17839": filasVentas(), "17851": filasClientes(),
				})
				my := nuevoMylosFalso(t)
				envFile := entorno(t, srv.URL, conMylos(t, my, map[string]string{falta: ""}))

				_, err := capturar(t, func() error {
					return run([]string{comando, "--from", "22/09/2026", "--to", "23/09/2026", "--env-file", envFile})
				})
				if err == nil {
					t.Fatal("esperaba error")
				}
				if !strings.Contains(err.Error(), falta) {
					t.Errorf("el error deberia nombrar %s: %v", falta, err)
				}
				if n := len(srv.vistos()); n != 0 {
					t.Errorf("no deberia haber consultado Tango: %d requests", n)
				}
			})
		}
	}
}

// El JSONL es ademas del POST, no en lugar del POST.
func TestPOST_ElJSONLNoReemplazaElEnvio(t *testing.T) {
	srv := nuevoTangoFalso(t, map[string][]map[string]any{
		"17839": filasVentas(), "17851": filasClientes(),
	})
	my := nuevoMylosFalso(t)
	envFile := entorno(t, srv.URL, conMylos(t, my, nil))
	dir := t.TempDir()

	if out, err := capturar(t, func() error {
		return run([]string{"sync", "--from", "22/09/2026", "--to", "23/09/2026",
			"--env-file", envFile, "--out-dir", dir})
	}); err != nil {
		t.Fatalf("sync: %v\n%s", err, out)
	}

	if n := len(my.recibidas()); n != 2 {
		t.Errorf("con --out-dir igual tiene que postear los dos datasets, hubo %d POST", n)
	}
	for _, archivo := range []string{"clientes.jsonl", "ventas.jsonl"} {
		b, err := os.ReadFile(filepath.Join(dir, archivo))
		if err != nil || len(b) == 0 {
			t.Errorf("%s: %v (len %d)", archivo, err, len(b))
		}
	}
}

// Un batch por pagina de Tango: el agente nunca junta el dataset entero.
func TestPOST_UnBatchPorPagina(t *testing.T) {
	var filas []map[string]any
	for i := 0; i < 5; i++ {
		f := filasClientes()[0]
		f["COD_CLIENTE"] = fmt.Sprintf("C%04d", i)
		filas = append(filas, f)
	}
	srv := nuevoTangoFalsoPaginado(t, map[string][]map[string]any{"17851": filas})
	my := nuevoMylosFalso(t)
	envFile := entorno(t, srv.URL, conMylos(t, my, nil))

	out, err := capturar(t, func() error {
		return run([]string{"sync-customers", "--from", "22/09/2026", "--to", "23/09/2026",
			"--env-file", envFile, "--page-size", "2"})
	})
	if err != nil {
		t.Fatalf("sync-customers: %v\n%s", err, out)
	}

	recibidas := my.recibidas()
	if len(recibidas) != 3 {
		t.Fatalf("5 filas con page-size 2 son 3 batches, hubo %d", len(recibidas))
	}
	var total int
	for i, in := range recibidas {
		rows, _ := in.batch["rows"].([]any)
		total += len(rows)
		if i < 2 && len(rows) != 2 {
			t.Errorf("batch %d: %d filas, esperaba 2", i, len(rows))
		}
	}
	if total != 5 {
		t.Errorf("llegaron %d filas en total, esperaba 5", total)
	}
	if !strings.Contains(out, "MYLOS batches   : 3 (5 filas)") {
		t.Errorf("el resumen deberia reportar 3 batches / 5 filas:\n%s", out)
	}
}
