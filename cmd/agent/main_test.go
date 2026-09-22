package main

import (
	"strings"
	"testing"
	"time"
)

func TestParseCLIDate(t *testing.T) {
	ok := map[string]string{
		"01/09/2026":  "2026-09-01", // DD/MM/AAAA, como se escribe en Tango
		"09/09/2026":  "2026-09-09",
		"2026-09-01":  "2026-09-01",
		" 31/12/2025": "2025-12-31",
	}
	for in, want := range ok {
		got, err := parseCLIDate(in)
		if err != nil {
			t.Errorf("parseCLIDate(%q): %v", in, err)
			continue
		}
		if got.Format("2006-01-02") != want {
			t.Errorf("parseCLIDate(%q) = %s, esperaba %s", in, got.Format("2006-01-02"), want)
		}
	}
	for _, in := range []string{"", "ayer", "2026/09/01", "31/31/2026", "09-01-2026"} {
		if _, err := parseCLIDate(in); err == nil {
			t.Errorf("parseCLIDate(%q) deberia fallar", in)
		}
	}
}

func TestRunComandoDesconocido(t *testing.T) {
	if err := run([]string{"no-existe"}); err == nil {
		t.Fatal("esperaba error")
	}
	if err := run(nil); err == nil {
		t.Fatal("esperaba error sin comando")
	}
	if err := run([]string{"version"}); err != nil {
		t.Fatalf("version: %v", err)
	}
}

func TestRangoSospechoso(t *testing.T) {
	from := time.Date(2026, 9, 19, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 9, 19, 0, 0, 0, 0, time.UTC)

	// Dentro del rango: sin aviso.
	if msg := rangoSospechoso("2026-09-19T00:00:00", "2026-09-19T23:59:00", from, to); msg != "" {
		t.Errorf("no deberia avisar: %s", msg)
	}
	// Sin datos con que comparar: sin aviso.
	if msg := rangoSospechoso("", "", from, to); msg != "" {
		t.Errorf("no deberia avisar sin fechas: %s", msg)
	}

	// El caso que motiva todo esto: se pide septiembre y vuelve enero.
	// Es exactamente la anomalia dd/MM vs MM/dd que hay que detectar.
	msg := rangoSospechoso("2026-01-02T00:00:00", "2026-01-02T00:00:00", from, to)
	if msg == "" {
		t.Fatal("deberia avisar: se pidio 19/09 y volvio 02/01")
	}
	for _, want := range []string{"2026-09-19", "2026-01-02", "fuera del rango"} {
		if !strings.Contains(msg, want) {
			t.Errorf("el aviso deberia mencionar %q: %s", want, msg)
		}
	}

	// Un solo dia desbordado alcanza para avisar.
	if msg := rangoSospechoso("2026-09-19T00:00:00", "2026-09-20T00:00:00", from, to); msg == "" {
		t.Error("deberia avisar si el maximo se pasa del rango")
	}
	if msg := rangoSospechoso("2026-09-18T00:00:00", "2026-09-19T00:00:00", from, to); msg == "" {
		t.Error("deberia avisar si el minimo es anterior al rango")
	}
}
