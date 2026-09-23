package mylos

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestUploader_UnBatchPorPagina(t *testing.T) {
	var recibidos []Batch
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b Batch
		_ = json.NewDecoder(r.Body).Decode(&b)
		recibidos = append(recibidos, b)
		_ = json.NewEncoder(w).Encode(BatchResponse{
			BatchID: 41, Received: len(b.Rows), Stored: len(b.Rows),
		})
	}))
	defer srv.Close()

	up := NewUploader(newTestClient(t, srv.URL, nil), DatasetCustomers, Meta{
		CompanyID: 2, ProcessID: 17851, CustomQueryID: "10",
		FromDate: "22/09/2026", ToDate: "23/09/2026",
	}, nil)

	pagina1 := []json.RawMessage{json.RawMessage(`{"a":1}`), json.RawMessage(`{"a":2}`)}
	pagina2 := []json.RawMessage{json.RawMessage(`{"a":3}`)}
	for _, p := range [][]json.RawMessage{pagina1, pagina2} {
		if err := up.SendPage(context.Background(), p); err != nil {
			t.Fatalf("SendPage: %v", err)
		}
	}

	if len(recibidos) != 2 {
		t.Fatalf("esperaba un batch por pagina, hubo %d", len(recibidos))
	}
	if len(recibidos[0].Rows) != 2 || len(recibidos[1].Rows) != 1 {
		t.Errorf("filas por batch = %d / %d", len(recibidos[0].Rows), len(recibidos[1].Rows))
	}
	// La metadata se repite igual en todos los batches.
	for i, b := range recibidos {
		if b.CompanyID != 2 || b.ProcessID != 17851 || b.CustomQueryID != "10" {
			t.Errorf("batch %d: metadata = %+v", i, b)
		}
		if b.FromDate != "22/09/2026" || b.ToDate != "23/09/2026" {
			t.Errorf("batch %d: fechas = %s -> %s", i, b.FromDate, b.ToDate)
		}
	}

	st := up.Stats()
	if st.Batches != 2 || st.Rows != 3 {
		t.Errorf("stats = %+v", st)
	}
	// Stored es cantidad de filas, no de batches.
	if st.RowsStored != 3 || st.Duplicates != 0 {
		t.Errorf("stats = %+v, esperaba 3 filas almacenadas y 0 duplicados", st)
	}
}

// Una pagina vacia no genera batch: no hay nada que ingestar.
func TestUploader_PaginaVaciaNoPostea(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_ = json.NewEncoder(w).Encode(BatchResponse{BatchID: 41, Stored: 1})
	}))
	defer srv.Close()

	up := NewUploader(newTestClient(t, srv.URL, nil), DatasetSales, Meta{}, nil)
	if err := up.SendPage(context.Background(), nil); err != nil {
		t.Fatalf("SendPage: %v", err)
	}
	if err := up.SendPage(context.Background(), []json.RawMessage{}); err != nil {
		t.Fatalf("SendPage: %v", err)
	}
	if calls != 0 {
		t.Errorf("no deberia haber posteado nada, hubo %d requests", calls)
	}
	if up.Stats().Batches != 0 {
		t.Errorf("stats = %+v", up.Stats())
	}
}

func TestUploader_ContabilizaDuplicados(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(BatchResponse{
			BatchID: 41, Received: 1, Stored: 0, Duplicate: true,
		})
	}))
	defer srv.Close()

	up := NewUploader(newTestClient(t, srv.URL, nil), DatasetSales, Meta{}, nil)
	if err := up.SendPage(context.Background(), []json.RawMessage{json.RawMessage(`{"a":1}`)}); err != nil {
		t.Fatalf("duplicate no deberia ser error: %v", err)
	}
	st := up.Stats()
	if st.Batches != 1 || st.Duplicates != 1 || st.RowsStored != 0 {
		t.Errorf("stats = %+v, un duplicado no almacena filas", st)
	}
	if st.LastBatchID != 41 {
		t.Errorf("batch id = %d", st.LastBatchID)
	}
}

func TestUploader_PropagaElError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "invalido", http.StatusUnprocessableEntity)
	}))
	defer srv.Close()

	up := NewUploader(newTestClient(t, srv.URL, nil), DatasetSales, Meta{}, nil)
	err := up.SendPage(context.Background(), []json.RawMessage{json.RawMessage(`{"a":1}`)})
	if err == nil {
		t.Fatal("esperaba error")
	}
	if up.Stats().Batches != 0 {
		t.Errorf("un batch fallido no se cuenta: %+v", up.Stats())
	}
}
