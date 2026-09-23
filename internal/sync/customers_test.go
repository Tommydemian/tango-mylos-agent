package sync

import (
	"context"
	"encoding/json"
	"testing"
)

// La consulta de clientes usa el mismo endpoint y solo cambia el process.
func TestFetchCustomers_UsaElProcessDeOptions(t *testing.T) {
	var processes []string
	srv := fakeTangoProcess(t, [][]map[string]any{
		{{"COD_CLIENTE": "C1"}},
	}, nil, nil, &processes)
	defer srv.Close()

	o := opts()
	o.ProcessID = "17851"
	if _, err := FetchCustomers(context.Background(), clientFor(t, srv.URL), o, nil, nil); err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if len(processes) != 1 || processes[0] != "17851" {
		t.Errorf("process enviado = %v, esperaba [17851]", processes)
	}
}

// No modelamos columnas de clientes: tiene que llegar el JSON completo,
// con campos que el agente no conoce ni va a conocer todavia.
func TestFetchCustomers_ConservaElJSONCompleto(t *testing.T) {
	fila := map[string]any{
		"COD_CLIENTE":    "C0001",
		"RAZON_SOCIAL":   "SACO NATALIA",
		"CUIT":           "20-12345678-9",
		"COLUMNA_RARA":   "valor que no modelamos",
		"OTRA_COLUMNA":   42.5,
		"LISTA_ANIDADA":  []any{"a", "b"},
		"OBJETO_ANIDADO": map[string]any{"k": "v"},
	}
	srv := fakeTangoProcess(t, [][]map[string]any{{fila}}, nil, nil, nil)
	defer srv.Close()

	o := opts()
	o.ProcessID = "17851"

	var got []json.RawMessage
	sum, err := FetchCustomers(context.Background(), clientFor(t, srv.URL), o, nil, func(rows []json.RawMessage) error {
		got = append(got, rows...)
		return nil
	})
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if sum.Rows != 1 || sum.Pages != 1 {
		t.Errorf("resumen = %+v", sum)
	}
	if len(got) != 1 {
		t.Fatalf("el sink recibio %d filas", len(got))
	}

	var m map[string]any
	if err := json.Unmarshal(got[0], &m); err != nil {
		t.Fatalf("la fila no es JSON valido: %v", err)
	}
	if len(m) != len(fila) {
		t.Errorf("se perdieron campos: llegaron %d de %d: %v", len(m), len(fila), m)
	}
	if m["COLUMNA_RARA"] != "valor que no modelamos" || m["OTRA_COLUMNA"] != 42.5 {
		t.Errorf("campos no modelados mal preservados: %v", m)
	}
	if _, ok := m["OBJETO_ANIDADO"].(map[string]any); !ok {
		t.Errorf("se perdio la estructura anidada: %v", m["OBJETO_ANIDADO"])
	}
}

func TestFetchCustomers_RecorreTodasLasPaginas(t *testing.T) {
	pages := [][]map[string]any{
		{{"COD_CLIENTE": "C1"}, {"COD_CLIENTE": "C2"}},
		{{"COD_CLIENTE": "C3"}},
	}
	var indexes []string
	srv := fakeTangoProcess(t, pages, nil, &indexes, nil)
	defer srv.Close()

	sum, err := FetchCustomers(context.Background(), clientFor(t, srv.URL), opts(), nil, nil)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if sum.Pages != 2 || sum.Rows != 3 {
		t.Errorf("resumen = %+v", sum)
	}
	if len(indexes) != 2 || indexes[0] != "0" || indexes[1] != "1" {
		t.Errorf("pageIndex pedidos = %v", indexes)
	}
}
