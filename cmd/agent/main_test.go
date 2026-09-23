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

// --- rango automatico (--days-back) -----------------------------------------

// Zona fija para los tests: no dependen de la zona de la maquina que corre CI,
// pero si verifican que se use la LOCAL y no UTC.
var zonaLocal = time.FixedZone("ART", -3*60*60)

func flagsRango(from, to string, daysBack int) *commonFlags {
	return &commonFlags{from: from, to: to, daysBack: daysBack}
}

func TestResolverRango_DaysBack(t *testing.T) {
	casos := []struct {
		nombre     string
		ahora      time.Time
		daysBack   int
		esperaFrom string // AAAA-MM-DD
		esperaTo   string
	}{
		{
			nombre:     "days-back 1 es ayer hasta hoy",
			ahora:      time.Date(2026, 9, 23, 14, 35, 0, 0, zonaLocal),
			daysBack:   1,
			esperaFrom: "2026-09-22",
			esperaTo:   "2026-09-23",
		},
		{
			nombre:     "days-back 0 es solo hoy",
			ahora:      time.Date(2026, 9, 23, 14, 35, 0, 0, zonaLocal),
			daysBack:   0,
			esperaFrom: "2026-09-23",
			esperaTo:   "2026-09-23",
		},
		{
			nombre:     "days-back 7",
			ahora:      time.Date(2026, 9, 23, 0, 5, 0, 0, zonaLocal),
			daysBack:   7,
			esperaFrom: "2026-09-16",
			esperaTo:   "2026-09-23",
		},
		{
			nombre:     "cambio de mes",
			ahora:      time.Date(2026, 3, 1, 9, 0, 0, 0, zonaLocal),
			daysBack:   1,
			esperaFrom: "2026-02-28", // 2026 no es bisiesto
			esperaTo:   "2026-03-01",
		},
		{
			nombre:     "cambio de mes hacia atras varios dias",
			ahora:      time.Date(2026, 5, 2, 23, 59, 0, 0, zonaLocal),
			daysBack:   5,
			esperaFrom: "2026-04-27",
			esperaTo:   "2026-05-02",
		},
		{
			nombre:     "cambio de anio",
			ahora:      time.Date(2027, 1, 1, 3, 0, 0, 0, zonaLocal),
			daysBack:   1,
			esperaFrom: "2026-12-31",
			esperaTo:   "2027-01-01",
		},
		{
			nombre:     "cambio de anio con varios dias",
			ahora:      time.Date(2027, 1, 2, 10, 0, 0, 0, zonaLocal),
			daysBack:   10,
			esperaFrom: "2026-12-23",
			esperaTo:   "2027-01-02",
		},
		{
			nombre:     "anio bisiesto",
			ahora:      time.Date(2028, 3, 1, 10, 0, 0, 0, zonaLocal),
			daysBack:   1,
			esperaFrom: "2028-02-29", // 2028 si es bisiesto
			esperaTo:   "2028-03-01",
		},
	}

	for _, caso := range casos {
		t.Run(caso.nombre, func(t *testing.T) {
			desde, hasta, aviso, err := resolverRango(flagsRango("", "", caso.daysBack), caso.ahora)
			if err != nil {
				t.Fatalf("resolverRango: %v", err)
			}
			if aviso != "" {
				t.Errorf("no deberia avisar nada: %s", aviso)
			}
			if got := desde.Format("2006-01-02"); got != caso.esperaFrom {
				t.Errorf("from = %s, esperaba %s", got, caso.esperaFrom)
			}
			if got := hasta.Format("2006-01-02"); got != caso.esperaTo {
				t.Errorf("to = %s, esperaba %s", got, caso.esperaTo)
			}
			// El desde arranca al principio del dia.
			if h, m, sec := desde.Clock(); h != 0 || m != 0 || sec != 0 {
				t.Errorf("from deberia ser el inicio del dia, es %02d:%02d:%02d", h, m, sec)
			}
		})
	}
}

// El rango se calcula en hora LOCAL, no en UTC. Se elige un momento en que la
// fecha local y la UTC son distintas: si usara UTC, el rango se corre un dia.
func TestResolverRango_UsaHoraLocalNoUTC(t *testing.T) {
	// 21:30 en ART (-03) del 1 de marzo = 00:30 UTC del 2 de marzo.
	ahora := time.Date(2026, 3, 1, 21, 30, 0, 0, zonaLocal)
	if ahora.UTC().Format("2006-01-02") != "2026-03-02" {
		t.Fatalf("el caso de prueba no discrimina: UTC = %s", ahora.UTC().Format("2006-01-02"))
	}

	desde, hasta, _, err := resolverRango(flagsRango("", "", 1), ahora)
	if err != nil {
		t.Fatalf("resolverRango: %v", err)
	}
	if got := desde.Format("2006-01-02"); got != "2026-02-28" {
		t.Errorf("from = %s, esperaba 2026-02-28 (en UTC daria 2026-03-01)", got)
	}
	if got := hasta.Format("2006-01-02"); got != "2026-03-01" {
		t.Errorf("to = %s, esperaba 2026-03-01 (en UTC daria 2026-03-02)", got)
	}
	if desde.Location() != zonaLocal {
		t.Errorf("from quedo en la zona %v, esperaba la local", desde.Location())
	}
}

// --from/--to explicitos le ganan a --days-back.
func TestResolverRango_FromToTienenPrioridad(t *testing.T) {
	ahora := time.Date(2026, 9, 23, 14, 0, 0, 0, zonaLocal)

	desde, hasta, aviso, err := resolverRango(flagsRango("01/09/2026", "09/09/2026", 30), ahora)
	if err != nil {
		t.Fatalf("resolverRango: %v", err)
	}
	if desde.Format("2006-01-02") != "2026-09-01" || hasta.Format("2006-01-02") != "2026-09-09" {
		t.Errorf("rango = %s -> %s, deberia mandar --from/--to",
			desde.Format("2006-01-02"), hasta.Format("2006-01-02"))
	}
	// Y no lo hace en silencio.
	if aviso == "" || !strings.Contains(aviso, "days-back") {
		t.Errorf("deberia avisar que se ignora --days-back, aviso = %q", aviso)
	}

	// Sin --days-back, ningun aviso.
	if _, _, aviso, err := resolverRango(flagsRango("01/09/2026", "09/09/2026", sinDaysBack), ahora); err != nil {
		t.Fatalf("resolverRango: %v", err)
	} else if aviso != "" {
		t.Errorf("no deberia avisar nada: %s", aviso)
	}
}

// Uno solo de --from/--to es error, aunque venga --days-back.
func TestResolverRango_FromYToVanJuntos(t *testing.T) {
	ahora := time.Date(2026, 9, 23, 14, 0, 0, 0, zonaLocal)

	casos := []struct {
		nombre  string
		cf      *commonFlags
		mencion string
	}{
		{"solo --from", flagsRango("01/09/2026", "", sinDaysBack), "--to"},
		{"solo --to", flagsRango("", "09/09/2026", sinDaysBack), "--from"},
		{"solo --from con days-back", flagsRango("01/09/2026", "", 1), "--to"},
		{"solo --to con days-back", flagsRango("", "09/09/2026", 1), "--from"},
	}
	for _, caso := range casos {
		t.Run(caso.nombre, func(t *testing.T) {
			_, _, _, err := resolverRango(caso.cf, ahora)
			if err == nil {
				t.Fatal("esperaba error")
			}
			if !strings.Contains(err.Error(), caso.mencion) {
				t.Errorf("el error deberia nombrar %s: %v", caso.mencion, err)
			}
		})
	}
}

func TestResolverRango_SinNingunModo(t *testing.T) {
	_, _, _, err := resolverRango(flagsRango("", "", sinDaysBack), time.Now())
	if err == nil {
		t.Fatal("esperaba error")
	}
	for _, want := range []string{"--from", "--to", "--days-back"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("el error deberia nombrar %s: %v", want, err)
		}
	}
}

func TestResolverRango_DaysBackNegativo(t *testing.T) {
	_, _, _, err := resolverRango(flagsRango("", "", -5), time.Now())
	if err == nil || !strings.Contains(err.Error(), "negativo") {
		t.Fatalf("esperaba error por days-back negativo, es: %v", err)
	}
}

func TestResolverRango_FechasInvalidas(t *testing.T) {
	ahora := time.Now()
	if _, _, _, err := resolverRango(flagsRango("ayer", "09/09/2026", sinDaysBack), ahora); err == nil {
		t.Error("--from invalido deberia fallar")
	}
	if _, _, _, err := resolverRango(flagsRango("01/09/2026", "manana", sinDaysBack), ahora); err == nil {
		t.Error("--to invalido deberia fallar")
	}
	if _, _, _, err := resolverRango(flagsRango("09/09/2026", "01/09/2026", sinDaysBack), ahora); err == nil {
		t.Error("--to anterior a --from deberia fallar")
	}
}
