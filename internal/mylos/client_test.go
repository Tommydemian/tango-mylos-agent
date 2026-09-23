package mylos

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mylos/mylos-tango-agent/internal/config"
)

const tokenDePrueba = "MYLOS-TOKEN-SUPER-SECRETO"

func newTestClient(t *testing.T, baseURL string, mutate func(*config.Config)) *Client {
	t.Helper()
	cfg := config.Config{
		MylosBaseURL:        baseURL,
		MylosIngestToken:    tokenDePrueba,
		MylosHTTPTimeout:    2 * time.Second,
		MylosMaxRetries:     0,
		MylosRetryBaseDelay: time.Millisecond,
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

func batchDePrueba() Batch {
	return Batch{
		CompanyID:     2,
		ProcessID:     17851,
		CustomQueryID: "10",
		FromDate:      "22/09/2026",
		ToDate:        "23/09/2026",
		Rows: []json.RawMessage{
			json.RawMessage(`{"COD_CLIENTE":"C0001","COLUMNA_RARA":"x"}`),
		},
	}
}

func okResponse(w http.ResponseWriter) {
	_ = json.NewEncoder(w).Encode(BatchResponse{
		BatchID: "b-123", Received: 1, Stored: true, Duplicate: false,
	})
}

func TestNew_ExigeConfigDeMylos(t *testing.T) {
	casos := map[string]config.Config{
		"sin base url": {MylosIngestToken: "x"},
		"sin token":    {MylosBaseURL: "https://api.mylos.app"},
		"sin nada":     {},
	}
	for nombre, cfg := range casos {
		t.Run(nombre, func(t *testing.T) {
			_, err := New(cfg, nil)
			if err == nil {
				t.Fatal("esperaba error")
			}
			if !strings.Contains(err.Error(), "MYLOS_") {
				t.Errorf("el error deberia nombrar la variable que falta: %v", err)
			}
		})
	}

	if _, err := New(config.Config{MylosBaseURL: "api.mylos.app", MylosIngestToken: "x"}, nil); err == nil {
		t.Error("una base url sin esquema deberia fallar")
	}
}

func TestDatasetPath(t *testing.T) {
	if got := DatasetCustomers.Path(); got != "/integrations/tango/customers/batch" {
		t.Errorf("customers path = %q", got)
	}
	if got := DatasetSales.Path(); got != "/integrations/tango/sales/batch" {
		t.Errorf("sales path = %q", got)
	}
}

func TestSendBatch_PathYHeaders(t *testing.T) {
	var gotPath, gotAuth, gotType, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		gotType = r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		okResponse(w)
	}))
	defer srv.Close()

	if _, err := newTestClient(t, srv.URL, nil).SendBatch(context.Background(), DatasetSales, batchDePrueba()); err != nil {
		t.Fatalf("SendBatch: %v", err)
	}
	if gotPath != "/integrations/tango/sales/batch" {
		t.Errorf("path = %q", gotPath)
	}
	if gotAuth != "Bearer "+tokenDePrueba {
		t.Errorf("Authorization mal armado: %q", gotAuth)
	}
	if gotType != "application/json" {
		t.Errorf("Content-Type = %q", gotType)
	}
	if !strings.Contains(gotBody, `"COLUMNA_RARA":"x"`) {
		t.Errorf("las filas no viajaron crudas: %s", gotBody)
	}
}

func TestSendBatch_BaseURLConBarraFinal(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		okResponse(w)
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL+"/", nil)
	if _, err := c.SendBatch(context.Background(), DatasetCustomers, batchDePrueba()); err != nil {
		t.Fatalf("SendBatch: %v", err)
	}
	if gotPath != "/integrations/tango/customers/batch" {
		t.Errorf("path = %q", gotPath)
	}
}

// duplicate=true con stored=false es exito, no error.
func TestSendBatch_DuplicateEsExito(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(BatchResponse{
			BatchID: "b-dup", Received: 1, Stored: false, Duplicate: true,
		})
	}))
	defer srv.Close()

	resp, err := newTestClient(t, srv.URL, nil).SendBatch(context.Background(), DatasetSales, batchDePrueba())
	if err != nil {
		t.Fatalf("duplicate no deberia ser error: %v", err)
	}
	if !resp.Duplicate || resp.Stored {
		t.Errorf("respuesta mal parseada: %+v", resp)
	}
	if resp.BatchID != "b-dup" || resp.Received != 1 {
		t.Errorf("respuesta mal parseada: %+v", resp)
	}
}

func TestSendBatch_NoReintenta4xx(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusUnprocessableEntity,
		http.StatusBadRequest, http.StatusForbidden} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				http.Error(w, "nope", status)
			}))
			defer srv.Close()

			_, err := newTestClient(t, srv.URL, func(c *config.Config) {
				c.MylosMaxRetries = 3
			}).SendBatch(context.Background(), DatasetSales, batchDePrueba())

			var httpErr *HTTPError
			if !errors.As(err, &httpErr) {
				t.Fatalf("esperaba *HTTPError, es %T: %v", err, err)
			}
			if httpErr.StatusCode != status {
				t.Errorf("status = %d", httpErr.StatusCode)
			}
			if n := calls.Load(); n != 1 {
				t.Errorf("%d no se reintenta; hubo %d requests", status, n)
			}
		})
	}
}

func TestSendBatch_Reintenta429Y5xx(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusInternalServerError,
		http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusRequestTimeout} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if calls.Add(1) < 3 {
					http.Error(w, "reintentame", status)
					return
				}
				okResponse(w)
			}))
			defer srv.Close()

			resp, err := newTestClient(t, srv.URL, func(c *config.Config) {
				c.MylosMaxRetries = 3
			}).SendBatch(context.Background(), DatasetSales, batchDePrueba())
			if err != nil {
				t.Fatalf("esperaba exito tras reintentos: %v", err)
			}
			if resp.BatchID != "b-123" {
				t.Errorf("respuesta = %+v", resp)
			}
			if n := calls.Load(); n != 3 {
				t.Errorf("esperaba 3 requests, hubo %d", n)
			}
		})
	}
}

// Error de red transitorio: el primer intento cae contra un server muerto.
func TestSendBatch_ReintentaErrorDeRed(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			// Cortar la conexion a mitad de respuesta.
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Fatal("el test necesita Hijacker")
			}
			conn, _, _ := hj.Hijack()
			conn.Close()
			return
		}
		okResponse(w)
	}))
	defer srv.Close()

	resp, err := newTestClient(t, srv.URL, func(c *config.Config) {
		c.MylosMaxRetries = 3
	}).SendBatch(context.Background(), DatasetSales, batchDePrueba())
	if err != nil {
		t.Fatalf("un corte de conexion deberia reintentarse: %v", err)
	}
	if resp.BatchID != "b-123" {
		t.Errorf("respuesta = %+v", resp)
	}
	if n := calls.Load(); n < 2 {
		t.Errorf("esperaba al menos 2 intentos, hubo %d", n)
	}
}

func TestSendBatch_Timeout(t *testing.T) {
	release := make(chan struct{})
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer func() { close(release); srv.Close() }()

	_, err := newTestClient(t, srv.URL, func(c *config.Config) {
		c.MylosHTTPTimeout = 50 * time.Millisecond
		c.MylosMaxRetries = 1
	}).SendBatch(context.Background(), DatasetSales, batchDePrueba())
	if err == nil {
		t.Fatal("esperaba timeout")
	}
	if !strings.Contains(err.Error(), "fallo la conexion") {
		t.Errorf("mensaje poco claro: %v", err)
	}
	if n := calls.Load(); n != 2 {
		t.Errorf("el timeout es transitorio y se reintenta; hubo %d requests", n)
	}
}

func TestSendBatch_RespuestaIlegible(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, "<html>upstream</html>")
	}))
	defer srv.Close()

	_, err := newTestClient(t, srv.URL, func(c *config.Config) {
		c.MylosMaxRetries = 2
	}).SendBatch(context.Background(), DatasetSales, batchDePrueba())

	var respErr *ResponseError
	if !errors.As(err, &respErr) {
		t.Fatalf("esperaba *ResponseError, es %T: %v", err, err)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("una respuesta ilegible no se reintenta; hubo %d requests", n)
	}
}

// El token nunca sale en un error, ni aunque el backend lo devuelva.
func TestErroresNoFiltranElToken(t *testing.T) {
	casos := map[string]http.HandlerFunc{
		"401 con el token en el body": func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "token rechazado: "+strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "),
				http.StatusUnauthorized)
		},
		"500": func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "boom", http.StatusInternalServerError)
		},
		"respuesta ilegible": func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, "nope")
		},
	}
	for nombre, h := range casos {
		t.Run(nombre, func(t *testing.T) {
			srv := httptest.NewServer(h)
			defer srv.Close()
			_, err := newTestClient(t, srv.URL, nil).SendBatch(context.Background(), DatasetSales, batchDePrueba())
			if err == nil {
				t.Fatal("esperaba error")
			}
			if strings.Contains(err.Error(), tokenDePrueba) {
				t.Fatalf("el error filtro el token: %v", err)
			}
		})
	}
}

func TestTargetNoExponeElToken(t *testing.T) {
	c := newTestClient(t, "https://api.mylos.app", nil)
	target := c.Target(DatasetSales)
	if strings.Contains(target, tokenDePrueba) {
		t.Fatalf("el target trae el token: %s", target)
	}
	if target != "https://api.mylos.app/integrations/tango/sales/batch" {
		t.Errorf("target = %s", target)
	}
}
