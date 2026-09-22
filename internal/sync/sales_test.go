package sync

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mylos/mylos-tango-agent/internal/config"
	"github.com/mylos/mylos-tango-agent/internal/model"
	"github.com/mylos/mylos-tango-agent/internal/tango"
)

// fakeTango sirve paginas fabricadas: pages[i] es la lista de la pagina i.
// hasNextPage se calcula igual que lo hace Tango.
func fakeTango(t *testing.T, pages [][]map[string]any, seen *atomic.Int32, indexes *[]string) *httptest.Server {
	t.Helper()
	total := 0
	for _, p := range pages {
		total += len(p)
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if seen != nil {
			seen.Add(1)
		}
		if indexes != nil {
			*indexes = append(*indexes, r.URL.Query().Get("pageIndex"))
		}
		idx := 0
		fmt.Sscanf(r.URL.Query().Get("pageIndex"), "%d", &idx)
		list := []map[string]any{}
		if idx >= 0 && idx < len(pages) {
			list = pages[idx]
		}
		resp := map[string]any{
			"resultData": map[string]any{
				"list":            list,
				"pageIndex":       idx,
				"pageSize":        len(list),
				"totalCount":      total,
				"totalPages":      len(pages),
				"hasPreviousPage": idx > 0,
				"hasNextPage":     idx < len(pages)-1,
			},
			"message":       nil,
			"exceptionInfo": nil,
			"succeeded":     true,
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

func row(nro, tipo, art string, cant, total float64, fecha string) map[string]any {
	return map[string]any{
		"FECHA_DE_EMISION": fecha,
		"TIPO_COMPROBANTE": tipo,
		"NRO_COMPROBANTE":  nro,
		"NOMBRE_VENDEDOR":  "VEND",
		"RAZON_SOCIAL":     "CLI",
		"COD_ARTICULO":     art,
		"DESCRIPCION":      "DESC " + art,
		"CANTIDAD":         cant,
		"TOTAL":            total,
	}
}

func clientFor(t *testing.T, baseURL string) *tango.Client {
	t.Helper()
	c, err := tango.New(config.Config{
		TangoBaseURL:        baseURL,
		TangoAPIToken:       "tok",
		TangoCompanyID:      "2",
		TangoSalesProcessID: "17839",
		HTTPTimeout:         2 * time.Second,
		MaxRetries:          1,
		RetryBaseDelay:      time.Millisecond,
		DateFormat:          "2006-01-02",
	}, nil)
	if err != nil {
		t.Fatalf("tango.New: %v", err)
	}
	return c
}

func opts() Options {
	return Options{ProcessID: "17839", FromDate: "2026-09-01", ToDate: "2026-09-09", PageSize: 2}
}

func TestFetchSales_RecorreTodasLasPaginas(t *testing.T) {
	pages := [][]map[string]any{
		{row("A1", "FAC", "ART1", 1, 100, "2026-09-02T00:00:00"), row("A1", "FAC", "ART2", 2, 200, "2026-09-02T00:00:00")},
		{row("A2", "FAC", "ART3", 3, 300, "2026-09-05T00:00:00"), row("A3", "NC", "ART4", 1, 50, "2026-09-01T00:00:00")},
		{row("A4", "FAC", "ART5", 1, 25, "2026-09-09T00:00:00")},
	}
	var indexes []string
	srv := fakeTango(t, pages, nil, &indexes)
	defer srv.Close()

	o := opts()
	o.SumAmounts = true

	var got []model.SalesLine
	sum, err := FetchSales(context.Background(), clientFor(t, srv.URL), o, nil, func(l model.SalesLine) error {
		got = append(got, l)
		return nil
	})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if sum.Pages != 3 {
		t.Errorf("paginas = %d, esperaba 3", sum.Pages)
	}
	if sum.Rows != 5 || len(got) != 5 {
		t.Errorf("filas = %d (sink recibio %d), esperaba 5", sum.Rows, len(got))
	}
	if want := []string{"0", "1", "2"}; strings.Join(indexes, ",") != strings.Join(want, ",") {
		t.Errorf("pageIndex pedidos = %v, esperaba %v (arranca en 0)", indexes, want)
	}
	if sum.Comprobantes != 4 {
		t.Errorf("comprobantes = %d, esperaba 4 (A1 aparece en 2 renglones)", sum.Comprobantes)
	}
	if sum.SumTotal != 675 {
		t.Errorf("suma total = %v, esperaba 675", sum.SumTotal)
	}
	if sum.SumCantidad != 8 {
		t.Errorf("suma cantidad = %v, esperaba 8", sum.SumCantidad)
	}
	if sum.TotalCountReported != 5 || sum.TotalPagesReported != 3 {
		t.Errorf("totales informados = %d/%d", sum.TotalCountReported, sum.TotalPagesReported)
	}
	if sum.RowsPorTipo["FAC"] != 4 || sum.RowsPorTipo["NC"] != 1 {
		t.Errorf("filas por tipo = %v", sum.RowsPorTipo)
	}
	if sum.MinFecha != "2026-09-01T00:00:00" || sum.MaxFecha != "2026-09-09T00:00:00" {
		t.Errorf("rango de fechas = %s -> %s", sum.MinFecha, sum.MaxFecha)
	}
	// Primera/ultima son en orden de aparicion, no ordenadas: las filas de
	// prueba vienen desordenadas a proposito.
	if sum.PrimeraFecha != "2026-09-02T00:00:00" {
		t.Errorf("primera fecha observada = %q", sum.PrimeraFecha)
	}
	if sum.UltimaFecha != "2026-09-09T00:00:00" {
		t.Errorf("ultima fecha observada = %q", sum.UltimaFecha)
	}
	if sum.Truncated {
		t.Error("no deberia estar truncado")
	}
}

// Por default no acumulamos importes: la semantica de TOTAL todavia no esta
// confirmada (impuestos, signo de las NC) y un numero incierto confunde mas
// de lo que aporta.
func TestFetchSales_NoSumaImportesPorDefault(t *testing.T) {
	pages := [][]map[string]any{
		{row("A1", "FAC", "ART1", 3, 100, "2026-09-02T00:00:00")},
	}
	srv := fakeTango(t, pages, nil, nil)
	defer srv.Close()

	sum, err := FetchSales(context.Background(), clientFor(t, srv.URL), opts(), nil, nil)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if sum.AmountsComputed {
		t.Error("AmountsComputed deberia ser false sin Options.SumAmounts")
	}
	if sum.SumTotal != 0 || sum.SumCantidad != 0 {
		t.Errorf("no deberia acumular importes: total=%v cantidad=%v", sum.SumTotal, sum.SumCantidad)
	}
	// Lo que si tiene que seguir estando:
	if sum.Rows != 1 || sum.Pages != 1 {
		t.Errorf("resumen = %+v", sum)
	}
	if sum.PrimeraFecha != "2026-09-02T00:00:00" || sum.UltimaFecha != "2026-09-02T00:00:00" {
		t.Errorf("fechas = %q / %q", sum.PrimeraFecha, sum.UltimaFecha)
	}
}

func TestFetchSales_CortaEnHasNextPageFalse(t *testing.T) {
	pages := [][]map[string]any{{row("A1", "FAC", "ART1", 1, 10, "2026-09-02T00:00:00")}}
	var calls atomic.Int32
	srv := fakeTango(t, pages, &calls, nil)
	defer srv.Close()

	sum, err := FetchSales(context.Background(), clientFor(t, srv.URL), opts(), nil, nil)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if sum.Pages != 1 || sum.Rows != 1 {
		t.Errorf("resumen = %+v", sum)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("con hasNextPage=false no se pide otra pagina; hubo %d requests", n)
	}
}

func TestFetchSales_RespetaMaxPages(t *testing.T) {
	pages := [][]map[string]any{
		{row("A1", "FAC", "ART1", 1, 10, "2026-09-02T00:00:00")},
		{row("A2", "FAC", "ART2", 1, 10, "2026-09-03T00:00:00")},
		{row("A3", "FAC", "ART3", 1, 10, "2026-09-04T00:00:00")},
	}
	var calls atomic.Int32
	srv := fakeTango(t, pages, &calls, nil)
	defer srv.Close()

	o := opts()
	o.MaxPages = 2
	sum, err := FetchSales(context.Background(), clientFor(t, srv.URL), o, nil, nil)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if sum.Pages != 2 || sum.Rows != 2 {
		t.Errorf("resumen = %+v", sum)
	}
	if !sum.Truncated {
		t.Error("deberia marcar Truncated")
	}
	if n := calls.Load(); n != 2 {
		t.Errorf("hubo %d requests, esperaba 2", n)
	}
}

// Un hasNextPage=true con lista vacia no debe dejar al agente girando para siempre.
func TestFetchSales_NoLoopeaConPaginaVaciaYHasNextTrue(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, `{"resultData":{"list":[],"pageIndex":0,"pageSize":2,"totalCount":99,"totalPages":50,"hasPreviousPage":false,"hasNextPage":true},"message":null,"exceptionInfo":null,"succeeded":true}`)
	}))
	defer srv.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		sum, err := FetchSales(context.Background(), clientFor(t, srv.URL), opts(), nil, nil)
		if err != nil {
			t.Errorf("error inesperado: %v", err)
		}
		if sum.Pages != 1 || sum.Rows != 0 {
			t.Errorf("resumen = %+v", sum)
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("FetchSales quedo en loop infinito (%d requests)", calls.Load())
	}
}

func TestFetchSales_PropagaErrorDeTango(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"resultData":null,"message":"no existe el proceso","exceptionInfo":null,"succeeded":false}`)
	}))
	defer srv.Close()

	sum, err := FetchSales(context.Background(), clientFor(t, srv.URL), opts(), nil, nil)
	if err == nil {
		t.Fatal("esperaba error")
	}
	if !strings.Contains(err.Error(), "pagina 0") {
		t.Errorf("el error deberia decir en que pagina fallo: %v", err)
	}
	if sum.Pages != 0 {
		t.Errorf("no deberia contar paginas fallidas: %+v", sum)
	}
}

func TestFetchSales_ErrorDelSinkCortaLaCorrida(t *testing.T) {
	pages := [][]map[string]any{
		{row("A1", "FAC", "ART1", 1, 10, "2026-09-02T00:00:00")},
		{row("A2", "FAC", "ART2", 1, 10, "2026-09-03T00:00:00")},
	}
	srv := fakeTango(t, pages, nil, nil)
	defer srv.Close()

	_, err := FetchSales(context.Background(), clientFor(t, srv.URL), opts(), nil, func(model.SalesLine) error {
		return fmt.Errorf("disco lleno")
	})
	if err == nil || !strings.Contains(err.Error(), "disco lleno") {
		t.Fatalf("esperaba el error del sink, es: %v", err)
	}
}

func TestFetchSales_PageSizeInvalido(t *testing.T) {
	o := opts()
	o.PageSize = 0
	if _, err := FetchSales(context.Background(), nil, o, nil, nil); err == nil {
		t.Fatal("esperaba error de validacion")
	}
}

// El JSONL conserva campos que el struct todavia no conoce (ej: COD_FAMILIA,
// que se va a agregar a la consulta Live mas adelante).
func TestJSONLWriter_ConservaElJSONOriginal(t *testing.T) {
	extra := row("A1", "FAC", "ART1", 1, 10, "2026-09-02T00:00:00")
	extra["COD_FAMILIA"] = "F12"
	extra["FAMILIA"] = "PERFUMERIA"
	srv := fakeTango(t, [][]map[string]any{{extra}}, nil, nil)
	defer srv.Close()

	path := filepath.Join(t.TempDir(), "out.jsonl")
	w, err := NewJSONLWriter(path)
	if err != nil {
		t.Fatalf("NewJSONLWriter: %v", err)
	}
	if _, err := FetchSales(context.Background(), clientFor(t, srv.URL), opts(), nil, w.Write); err != nil {
		t.Fatalf("FetchSales: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	lines := 0
	for sc.Scan() {
		lines++
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("linea %d no es JSON valido: %v", lines, err)
		}
		if m["COD_FAMILIA"] != "F12" || m["FAMILIA"] != "PERFUMERIA" {
			t.Errorf("se perdieron campos no modelados: %v", m)
		}
		if m["NRO_COMPROBANTE"] != "A1" {
			t.Errorf("fila mal volcada: %v", m)
		}
	}
	if lines != 1 {
		t.Errorf("lineas = %d, esperaba 1", lines)
	}
}
