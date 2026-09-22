package tango

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mylos/mylos-tango-agent/internal/config"
	"github.com/mylos/mylos-tango-agent/internal/model"
)

const testToken = "TOKEN-SUPER-SECRETO-123"

// respuesta real confirmada del servidor Tango (una fila).
const realResponse = `{
  "resultData": {
    "list": [
      {
        "FECHA_DE_EMISION": "2026-01-02T00:00:00",
        "TIPO_COMPROBANTE": "FAC",
        "NRO_COMPROBANTE": "B0001500020858",
        "NOMBRE_VENDEDOR": "GABRIELA CONTARTESE",
        "RAZON_SOCIAL": "SACO NATALIA",
        "COD_ARTICULO": "PAT015-029",
        "DESCRIPCION": "TEXTIL VERBENA 1,5L",
        "CANTIDAD": 1,
        "TOTAL": 48760.330579
      }
    ],
    "pageIndex": 0,
    "pageSize": 10,
    "totalCount": 21812,
    "totalPages": 2182,
    "hasPreviousPage": false,
    "hasNextPage": true
  },
  "message": null,
  "exceptionInfo": null,
  "succeeded": true
}`

func newTestClient(t *testing.T, baseURL string, mutate func(*config.Config)) *Client {
	t.Helper()
	cfg := config.Config{
		TangoBaseURL:        baseURL,
		TangoAPIToken:       testToken,
		TangoCompanyID:      "2",
		TangoSalesProcessID: "17839",
		HTTPTimeout:         2 * time.Second,
		MaxRetries:          0,
		RetryBaseDelay:      time.Millisecond,
		DateFormat:          "2006-01-02",
	}
	if mutate != nil {
		mutate(&cfg)
	}
	c, err := New(cfg, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func defaultParams() QueryParams {
	return QueryParams{
		Process:   "17839",
		FromDate:  "2026-09-01",
		ToDate:    "2026-09-09",
		PageSize:  10,
		PageIndex: 0,
	}
}

func TestGetApiLiveQueryData_ParseaRespuestaReal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, realResponse)
	}))
	defer srv.Close()

	page, err := GetApiLiveQueryData[model.SalesLine](context.Background(), newTestClient(t, srv.URL, nil), defaultParams())
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if len(page.List) != 1 {
		t.Fatalf("list: esperaba 1 fila, hay %d", len(page.List))
	}
	row := page.List[0]
	if row.NroComprobante != "B0001500020858" || row.TipoComprobante != "FAC" {
		t.Errorf("comprobante mal parseado: %+v", row)
	}
	if row.CodArticulo != "PAT015-029" || row.Descripcion != "TEXTIL VERBENA 1,5L" {
		t.Errorf("articulo mal parseado: %+v", row)
	}
	if row.NombreVendedor != "GABRIELA CONTARTESE" || row.RazonSocial != "SACO NATALIA" {
		t.Errorf("vendedor/cliente mal parseado: %+v", row)
	}
	if row.FechaDeEmision != "2026-01-02T00:00:00" {
		t.Errorf("fecha = %q", row.FechaDeEmision)
	}
	if row.Cantidad != 1 {
		t.Errorf("cantidad = %v", row.Cantidad)
	}
	if row.Total != 48760.330579 {
		t.Errorf("total = %v (se perdio precision?)", row.Total)
	}
	if len(row.Raw) == 0 || !strings.Contains(string(row.Raw), "PAT015-029") {
		t.Errorf("Raw no conservo el JSON original: %s", row.Raw)
	}
	if page.PageIndex != 0 || page.PageSize != 10 || page.TotalCount != 21812 || page.TotalPages != 2182 {
		t.Errorf("paginado mal parseado: %+v", page)
	}
	if page.HasPreviousPage || !page.HasNextPage {
		t.Errorf("flags de paginado mal parseados: %+v", page)
	}
}

func TestGetApiLiveQueryData_MandaHeadersYParams(t *testing.T) {
	var got *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Clone(context.Background())
		fmt.Fprint(w, realResponse)
	}))
	defer srv.Close()

	p := defaultParams()
	p.PageIndex = 3
	p.PageSize = 500
	p.CustomQuery = "algo"
	if _, err := GetApiLiveQueryData[model.SalesLine](context.Background(), newTestClient(t, srv.URL, nil), p); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}

	if got.URL.Path != "/Api/GetApiLiveQueryData" {
		t.Errorf("path = %q", got.URL.Path)
	}
	q := got.URL.Query()
	for k, want := range map[string]string{
		"process":     "17839",
		"fromDate":    "2026-09-01",
		"toDate":      "2026-09-09",
		"pageSize":    "500",
		"pageIndex":   "3",
		"customQuery": "algo",
	} {
		if q.Get(k) != want {
			t.Errorf("query %s = %q, esperaba %q", k, q.Get(k), want)
		}
	}
	if got.Header.Get("ApiAuthorization") != testToken {
		t.Errorf("no se mando ApiAuthorization")
	}
	if got.Header.Get("Company") != "2" {
		t.Errorf("Company = %q", got.Header.Get("Company"))
	}
	if got.Header.Get("Accept") != "application/json" {
		t.Errorf("Accept = %q", got.Header.Get("Accept"))
	}
}

func TestGetApiLiveQueryData_CustomQueryOmitidoSiVacio(t *testing.T) {
	var raw string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw = r.URL.RawQuery
		fmt.Fprint(w, realResponse)
	}))
	defer srv.Close()

	if _, err := GetApiLiveQueryData[model.SalesLine](context.Background(), newTestClient(t, srv.URL, nil), defaultParams()); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if strings.Contains(raw, "customQuery") {
		t.Errorf("customQuery no deberia viajar si esta vacio: %s", raw)
	}
}

func TestGetApiLiveQueryData_BaseURLConPathYBarraFinal(t *testing.T) {
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		fmt.Fprint(w, realResponse)
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL+"/", nil)
	if _, err := GetApiLiveQueryData[model.SalesLine](context.Background(), c, defaultParams()); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if path != "/Api/GetApiLiveQueryData" {
		t.Errorf("path = %q", path)
	}
}

func TestGetApiLiveQueryData_SucceededFalse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"resultData":null,"message":"proceso inexistente","exceptionInfo":"stack...","succeeded":false}`)
	}))
	defer srv.Close()

	var calls atomic.Int32
	srv.Config.Handler = countingHandler(&calls, srv.Config.Handler)

	_, err := GetApiLiveQueryData[model.SalesLine](context.Background(), newTestClient(t, srv.URL, func(c *config.Config) {
		c.MaxRetries = 2
	}), defaultParams())
	if err == nil {
		t.Fatal("esperaba error con succeeded=false")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("esperaba *APIError, es %T: %v", err, err)
	}
	if apiErr.Message != "proceso inexistente" {
		t.Errorf("message = %q", apiErr.Message)
	}
	if apiErr.ExceptionInfo != "stack..." {
		t.Errorf("exceptionInfo = %q", apiErr.ExceptionInfo)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("succeeded=false no se reintenta; hubo %d requests", n)
	}
}

func TestGetApiLiveQueryData_ResultDataNull(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"resultData":null,"message":null,"exceptionInfo":null,"succeeded":true}`)
	}))
	defer srv.Close()

	_, err := GetApiLiveQueryData[model.SalesLine](context.Background(), newTestClient(t, srv.URL, nil), defaultParams())
	var respErr *ResponseError
	if !errors.As(err, &respErr) {
		t.Fatalf("esperaba *ResponseError, es %T: %v", err, err)
	}
}

func TestGetApiLiveQueryData_JSONInvalido(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(countingHandler(&calls, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<html>login</html>`)
	})))
	defer srv.Close()

	_, err := GetApiLiveQueryData[model.SalesLine](context.Background(), newTestClient(t, srv.URL, func(c *config.Config) {
		c.MaxRetries = 2
	}), defaultParams())
	var respErr *ResponseError
	if !errors.As(err, &respErr) {
		t.Fatalf("esperaba *ResponseError, es %T: %v", err, err)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("JSON invalido no se reintenta; hubo %d requests", n)
	}
}

func TestGetApiLiveQueryData_ReintentaEn5xxYDespuesFunciona(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		fmt.Fprint(w, realResponse)
	}))
	defer srv.Close()

	page, err := GetApiLiveQueryData[model.SalesLine](context.Background(), newTestClient(t, srv.URL, func(c *config.Config) {
		c.MaxRetries = 3
	}), defaultParams())
	if err != nil {
		t.Fatalf("esperaba exito tras reintentos: %v", err)
	}
	if len(page.List) != 1 {
		t.Errorf("list = %d filas", len(page.List))
	}
	if n := calls.Load(); n != 3 {
		t.Errorf("esperaba 3 requests (2 fallidos + 1 ok), hubo %d", n)
	}
}

func TestGetApiLiveQueryData_AgotaReintentosEn5xx(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(countingHandler(&calls, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusBadGateway)
	})))
	defer srv.Close()

	_, err := GetApiLiveQueryData[model.SalesLine](context.Background(), newTestClient(t, srv.URL, func(c *config.Config) {
		c.MaxRetries = 2
	}), defaultParams())
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("esperaba *HTTPError, es %T: %v", err, err)
	}
	if httpErr.StatusCode != http.StatusBadGateway {
		t.Errorf("status = %d", httpErr.StatusCode)
	}
	if n := calls.Load(); n != 3 {
		t.Errorf("esperaba 3 intentos (1 + 2 reintentos), hubo %d", n)
	}
}

func TestGetApiLiveQueryData_NoReintentaEn4xx(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(countingHandler(&calls, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "token invalido", http.StatusUnauthorized)
	})))
	defer srv.Close()

	_, err := GetApiLiveQueryData[model.SalesLine](context.Background(), newTestClient(t, srv.URL, func(c *config.Config) {
		c.MaxRetries = 3
	}), defaultParams())
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("esperaba *HTTPError, es %T: %v", err, err)
	}
	if httpErr.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d", httpErr.StatusCode)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("401 no se reintenta; hubo %d requests", n)
	}
}

func TestGetApiLiveQueryData_Reintenta429(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			http.Error(w, "slow down", http.StatusTooManyRequests)
			return
		}
		fmt.Fprint(w, realResponse)
	}))
	defer srv.Close()

	if _, err := GetApiLiveQueryData[model.SalesLine](context.Background(), newTestClient(t, srv.URL, func(c *config.Config) {
		c.MaxRetries = 2
	}), defaultParams()); err != nil {
		t.Fatalf("esperaba exito tras 429: %v", err)
	}
	if n := calls.Load(); n != 2 {
		t.Errorf("esperaba 2 requests, hubo %d", n)
	}
}

func TestGetApiLiveQueryData_Timeout(t *testing.T) {
	release := make(chan struct{})
	var calls atomic.Int32
	srv := httptest.NewServer(countingHandler(&calls, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})))
	defer func() {
		close(release)
		srv.Close()
	}()

	start := time.Now()
	_, err := GetApiLiveQueryData[model.SalesLine](context.Background(), newTestClient(t, srv.URL, func(c *config.Config) {
		c.HTTPTimeout = 50 * time.Millisecond
		c.MaxRetries = 1
	}), defaultParams())
	if err == nil {
		t.Fatal("esperaba error de timeout")
	}
	if !strings.Contains(err.Error(), "fallo la conexion") {
		t.Errorf("mensaje poco claro: %v", err)
	}
	if n := calls.Load(); n != 2 {
		t.Errorf("el timeout es transitorio y se reintenta; hubo %d requests", n)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("tardo demasiado en rendirse: %s", elapsed)
	}
}

func TestGetApiLiveQueryData_ContextoCanceladoNoReintenta(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(countingHandler(&calls, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	if _, err := GetApiLiveQueryData[model.SalesLine](ctx, newTestClient(t, srv.URL, func(c *config.Config) {
		c.MaxRetries = 5
	}), defaultParams()); err == nil {
		t.Fatal("esperaba error")
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("con el ctx cancelado no se reintenta; hubo %d requests", n)
	}
}

// El token jamas debe aparecer en un error, pase lo que pase del otro lado.
func TestErroresNoFiltranElToken(t *testing.T) {
	cases := map[string]http.HandlerFunc{
		"401 con el token en el body": func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "token rechazado: "+r.Header.Get("ApiAuthorization"), http.StatusUnauthorized)
		},
		"500": func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "boom", http.StatusInternalServerError)
		},
		"succeeded=false": func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"resultData":null,"message":"no","exceptionInfo":null,"succeeded":false}`)
		},
		"json invalido": func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `nope`)
		},
	}
	for name, h := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(h)
			defer srv.Close()
			_, err := GetApiLiveQueryData[model.SalesLine](context.Background(), newTestClient(t, srv.URL, nil), defaultParams())
			if err == nil {
				t.Fatal("esperaba error")
			}
			if strings.Contains(err.Error(), testToken) {
				t.Fatalf("el error filtro el token: %v", err)
			}
		})
	}
}

// El caso "401 con el token en el body" es el unico donde el token podria
// volver desde el server; el cliente lo enmascara antes de armar el error.
func TestHTTPErrorBodyEnmascaraElToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "token rechazado: "+r.Header.Get("ApiAuthorization"), http.StatusUnauthorized)
	}))
	defer srv.Close()

	_, err := GetApiLiveQueryData[model.SalesLine](context.Background(), newTestClient(t, srv.URL, nil), defaultParams())
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("esperaba *HTTPError: %v", err)
	}
	if strings.Contains(httpErr.Body, testToken) {
		t.Fatalf("el body del error contiene el token: %q", httpErr.Body)
	}
}

func TestQueryParamsInvalidos(t *testing.T) {
	c := newTestClient(t, "http://localhost:1", nil)
	cases := map[string]QueryParams{
		"sin process":        {PageSize: 10},
		"pageSize cero":      {Process: "1", PageSize: 0},
		"pageIndex negativo": {Process: "1", PageSize: 10, PageIndex: -1},
	}
	for name, p := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := GetApiLiveQueryData[model.SalesLine](context.Background(), c, p); err == nil {
				t.Fatal("esperaba error de validacion")
			}
		})
	}
}

func countingHandler(n *atomic.Int32, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		next.ServeHTTP(w, r)
	})
}
