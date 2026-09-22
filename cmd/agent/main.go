// Command mylos-tango-agent es el agente on-premise que lee Tango.
// FASE 1: solo lectura de la consulta Live de ventas + resumen.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/mylos/mylos-tango-agent/internal/config"
	"github.com/mylos/mylos-tango-agent/internal/sync"
	"github.com/mylos/mylos-tango-agent/internal/tango"
)

// version lo setea el build: -ldflags "-X main.version=..."
var version = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		usage()
		return errors.New("falta el comando")
	}
	switch args[0] {
	case "sync-sales":
		return runSyncSales(args[1:])
	case "version", "--version", "-v":
		fmt.Println("mylos-tango-agent", version)
		return nil
	case "help", "--help", "-h":
		usage()
		return nil
	default:
		usage()
		return fmt.Errorf("comando desconocido: %q", args[0])
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `mylos-tango-agent - agente on-premise Tango -> MYLOS

Uso:
  mylos-tango-agent sync-sales --from DD/MM/AAAA --to DD/MM/AAAA [opciones]
  mylos-tango-agent version

Configuracion: variables de entorno (ver .env.example).
`)
}

func runSyncSales(args []string) error {
	fs := flag.NewFlagSet("sync-sales", flag.ContinueOnError)
	var (
		from        = fs.String("from", "", "fecha desde (DD/MM/AAAA o AAAA-MM-DD) [obligatoria]")
		to          = fs.String("to", "", "fecha hasta (DD/MM/AAAA o AAAA-MM-DD) [obligatoria]")
		pageSize    = fs.Int("page-size", 500, "filas por pagina")
		maxPages    = fs.Int("max-pages", 0, "cortar despues de N paginas (0 = todas)")
		out         = fs.String("out", "", "archivo JSONL de inspeccion (opt-in). CONTIENE DATOS REALES: clientes, vendedores, precios")
		sumAmounts  = fs.Bool("sum-amounts", false, "acumular TOTAL y CANTIDAD en el resumen (semantica de TOTAL aun no confirmada)")
		customQuery = fs.String("custom-query", "", "parametro customQuery de la consulta Live (opcional)")
		envFile     = fs.String("env-file", ".env", "archivo de configuracion a precargar (si existe)")
		logLevel    = fs.String("log-level", "info", "debug|info|warn|error")
		logFormat   = fs.String("log-format", "text", "text|json")
		timeout     = fs.Duration("timeout", 0, "tiempo maximo de toda la corrida (0 = sin limite)")
	)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Uso: mylos-tango-agent sync-sales --from DD/MM/AAAA --to DD/MM/AAAA [opciones]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *from == "" || *to == "" {
		fs.Usage()
		return errors.New("--from y --to son obligatorios")
	}

	fromDate, err := parseCLIDate(*from)
	if err != nil {
		return fmt.Errorf("--from: %w", err)
	}
	toDate, err := parseCLIDate(*to)
	if err != nil {
		return fmt.Errorf("--to: %w", err)
	}
	if toDate.Before(fromDate) {
		return errors.New("--to no puede ser anterior a --from")
	}

	if err := config.LoadEnvFile(*envFile, false); err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	log := newLogger(*logLevel, *logFormat)
	log.Debug("configuracion cargada", slog.Any("config", cfg))

	client, err := tango.New(cfg, log)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *timeout)
		defer cancel()
	}

	var sink sync.Sink
	if *out != "" {
		w, err := sync.NewJSONLWriter(*out)
		if err != nil {
			return err
		}
		log.Warn("el archivo de salida va a contener datos reales de negocio "+
			"(razon social, vendedor, articulos, importes): tratalo como dato sensible",
			slog.String("out", *out))
		defer func() {
			if cerr := w.Close(); cerr != nil {
				fmt.Fprintln(os.Stderr, "error cerrando la salida:", cerr)
			}
		}()
		sink = w.Write
	}

	opts := sync.Options{
		ProcessID:   cfg.TangoSalesProcessID,
		FromDate:    fromDate.Format(cfg.DateFormat),
		ToDate:      toDate.Format(cfg.DateFormat),
		PageSize:    *pageSize,
		MaxPages:    *maxPages,
		CustomQuery: *customQuery,
		SumAmounts:  *sumAmounts,
	}
	log.Info("iniciando lectura de ventas",
		slog.String("process", opts.ProcessID),
		slog.String("from_date", opts.FromDate),
		slog.String("to_date", opts.ToDate),
		slog.Int("page_size", opts.PageSize),
	)

	summary, err := sync.FetchSales(ctx, client, opts, log, sink)
	defer func() {
		// Diagnostico del unknown abierto: si las fechas que devolvio Tango
		// caen fuera del rango pedido, el filtro no hace lo que asumimos.
		if msg := rangoSospechoso(summary.MinFecha, summary.MaxFecha, fromDate, toDate); msg != "" {
			fmt.Fprintln(os.Stderr, msg)
		}
	}()
	if err != nil {
		// Si alcanzo a leer algo, el resumen parcial sirve para diagnosticar.
		if summary.Pages > 0 {
			printSummary(summary, opts, *out)
		}
		return err
	}
	printSummary(summary, opts, *out)
	return nil
}

func printSummary(s sync.Summary, opts sync.Options, out string) {
	fmt.Println()
	fmt.Println("== Resumen sync-sales ==")
	fmt.Printf("  rango consultado   : %s -> %s\n", opts.FromDate, opts.ToDate)
	fmt.Printf("  paginas leidas     : %d\n", s.Pages)
	fmt.Printf("  filas (renglones)  : %d\n", s.Rows)
	// FECHA_DE_EMISION en orden de aparicion, no ordenada.
	fmt.Printf("  1ra fecha emision  : %s\n", orGuion(s.PrimeraFecha))
	fmt.Printf("  ult fecha emision  : %s\n", orGuion(s.UltimaFecha))
	if s.MinFecha != s.PrimeraFecha || s.MaxFecha != s.UltimaFecha {
		fmt.Printf("  min/max observados : %s -> %s (el resultado no vino ordenado)\n", s.MinFecha, s.MaxFecha)
	}
	fmt.Printf("  totalCount Tango   : %d (totalPages %d)\n", s.TotalCountReported, s.TotalPagesReported)
	fmt.Printf("  comprobantes       : %d\n", s.Comprobantes)
	if len(s.RowsPorTipo) > 0 {
		var parts []string
		for _, t := range s.TiposOrdenados() {
			parts = append(parts, fmt.Sprintf("%s=%d", t, s.RowsPorTipo[t]))
		}
		fmt.Printf("  filas por tipo     : %s\n", strings.Join(parts, " "))
	}
	if s.AmountsComputed {
		fmt.Printf("  suma CANTIDAD      : %.2f\n", s.SumCantidad)
		fmt.Printf("  suma TOTAL         : %.2f (orientativo: falta confirmar impuestos y signo de NC)\n", s.SumTotal)
	}
	fmt.Printf("  duracion           : %s\n", s.Elapsed.Round(time.Millisecond))
	if out != "" {
		fmt.Printf("  salida JSONL       : %s\n", out)
	}
	if s.Truncated {
		fmt.Println("  AVISO: corte por --max-pages, el resumen es parcial")
	}
	if s.TotalCountReported > 0 && !s.Truncated && s.Rows != s.TotalCountReported {
		fmt.Printf("  AVISO: filas leidas (%d) != totalCount informado (%d).\n", s.Rows, s.TotalCountReported)
		fmt.Println("         Puede ser actividad en Tango durante la paginacion (ver README).")
	}
	fmt.Println()
}

// rangoSospechoso compara las fechas observadas contra el rango pedido usando
// solo el prefijo AAAA-MM-DD, sin interpretar hora ni zona (no sabemos que
// representan). Devuelve "" si todo cierra o si no hay con que comparar.
func rangoSospechoso(minFecha, maxFecha string, from, to time.Time) string {
	if len(minFecha) < 10 || len(maxFecha) < 10 {
		return ""
	}
	desde := from.Format("2006-01-02")
	hasta := to.Format("2006-01-02")
	if minFecha[:10] >= desde && maxFecha[:10] <= hasta {
		return ""
	}
	return fmt.Sprintf(
		"\nAVISO IMPORTANTE: Tango devolvio filas con FECHA_DE_EMISION fuera del rango pedido.\n"+
			"  pedido    : %s -> %s\n"+
			"  observado : %s -> %s\n"+
			"  El filtro de fechas no se comporta como asumimos (formato de fromDate/toDate\n"+
			"  o semantica del endpoint). Parar e investigar antes de seguir. Ver README,\n"+
			"  seccion \"Como demostrar el filtro de fechas\".",
		desde, hasta, minFecha[:10], maxFecha[:10])
}

func orGuion(v string) string {
	if v == "" {
		return "-"
	}
	return v
}

// parseCLIDate acepta DD/MM/AAAA (como se usa en Tango) y AAAA-MM-DD.
func parseCLIDate(v string) (time.Time, error) {
	v = strings.TrimSpace(v)
	for _, layout := range []string{"02/01/2006", "2006-01-02"} {
		if t, err := time.Parse(layout, v); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("fecha invalida %q: usar DD/MM/AAAA o AAAA-MM-DD", v)
}

func newLogger(level, format string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: lvl}
	if strings.ToLower(format) == "json" {
		return slog.New(slog.NewJSONHandler(os.Stderr, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stderr, opts))
}
