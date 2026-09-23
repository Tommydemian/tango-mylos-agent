// Command mylos-tango-agent es el agente on-premise que lee Tango.
// FASE 1: solo lectura de consultas Live + resumen. No escribe en MYLOS.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/mylos/mylos-tango-agent/internal/config"
	"github.com/mylos/mylos-tango-agent/internal/sync"
	"github.com/mylos/mylos-tango-agent/internal/tango"
)

// version lo setea el build: -ldflags "-X main.version=..."
var version = "dev"

// Salidas del proceso, como variables para poder capturarlas en los tests.
var (
	stdout io.Writer = os.Stdout
	stderr io.Writer = os.Stderr
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(stderr, "error:", err)
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
	case "sync-customers":
		return runSyncCustomers(args[1:])
	case "sync":
		return runSync(args[1:])
	case "version", "--version", "-v":
		fmt.Fprintln(stdout, "mylos-tango-agent", version)
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
	fmt.Fprint(stderr, `mylos-tango-agent - agente on-premise Tango -> MYLOS

Uso:
  mylos-tango-agent sync-sales     --from DD/MM/AAAA --to DD/MM/AAAA [opciones]
  mylos-tango-agent sync-customers --from DD/MM/AAAA --to DD/MM/AAAA [opciones]
  mylos-tango-agent sync           --from DD/MM/AAAA --to DD/MM/AAAA [opciones]
  mylos-tango-agent version

  sync corre las dos consultas en secuencia: primero clientes, despues ventas.

Configuracion: variables de entorno (ver .env.example).
`)
}

// commonFlags son los flags que comparten los tres comandos.
type commonFlags struct {
	from        string
	to          string
	pageSize    int
	maxPages    int
	customQuery string
	envFile     string
	logLevel    string
	logFormat   string
	timeout     time.Duration
	sumAmounts  bool
}

func bindCommon(fs *flag.FlagSet) *commonFlags {
	cf := &commonFlags{}
	fs.StringVar(&cf.from, "from", "", "fecha desde (DD/MM/AAAA o AAAA-MM-DD) [obligatoria]")
	fs.StringVar(&cf.to, "to", "", "fecha hasta (DD/MM/AAAA o AAAA-MM-DD) [obligatoria]")
	fs.IntVar(&cf.pageSize, "page-size", 500, "filas por pagina")
	fs.IntVar(&cf.maxPages, "max-pages", 0, "cortar despues de N paginas (0 = todas)")
	fs.StringVar(&cf.customQuery, "custom-query", "", "parametro customQuery de la consulta Live (opcional)")
	fs.StringVar(&cf.envFile, "env-file", ".env", "archivo de configuracion a precargar (si existe)")
	fs.StringVar(&cf.logLevel, "log-level", "info", "debug|info|warn|error")
	fs.StringVar(&cf.logFormat, "log-format", "text", "text|json")
	fs.DurationVar(&cf.timeout, "timeout", 0, "tiempo maximo de toda la corrida (0 = sin limite)")
	fs.BoolVar(&cf.sumAmounts, "sum-amounts", false, "acumular TOTAL y CANTIDAD en el resumen de ventas (semantica de TOTAL aun no confirmada)")
	return cf
}

// session es todo lo que necesita un comando despues de parsear los flags.
type session struct {
	cfg      config.Config
	log      *slog.Logger
	client   *tango.Client
	ctx      context.Context
	stop     func()
	from, to time.Time
}

func newSession(cf *commonFlags) (*session, error) {
	if cf.from == "" || cf.to == "" {
		return nil, errors.New("--from y --to son obligatorios")
	}
	from, err := parseCLIDate(cf.from)
	if err != nil {
		return nil, fmt.Errorf("--from: %w", err)
	}
	to, err := parseCLIDate(cf.to)
	if err != nil {
		return nil, fmt.Errorf("--to: %w", err)
	}
	if to.Before(from) {
		return nil, errors.New("--to no puede ser anterior a --from")
	}

	if err := config.LoadEnvFile(cf.envFile, false); err != nil {
		return nil, err
	}
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}

	log := newLogger(cf.logLevel, cf.logFormat)
	log.Debug("configuracion cargada", slog.Any("config", cfg))

	client, err := tango.New(cfg, log)
	if err != nil {
		return nil, err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	if cf.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cf.timeout)
		prev := stop
		stop = func() { cancel(); prev() }
	}
	return &session{cfg: cfg, log: log, client: client, ctx: ctx, stop: stop, from: from, to: to}, nil
}

// options arma las Options de una consulta para el process indicado.
func (s *session) options(processID string, cf *commonFlags) sync.Options {
	return sync.Options{
		ProcessID:   processID,
		FromDate:    s.from.Format(s.cfg.DateFormat),
		ToDate:      s.to.Format(s.cfg.DateFormat),
		PageSize:    cf.pageSize,
		MaxPages:    cf.maxPages,
		CustomQuery: cf.customQuery,
		SumAmounts:  cf.sumAmounts,
	}
}

// openSink abre el JSONL de salida si se pidio. Devuelve siempre un close().
func (s *session) openSink(path string) (sync.Sink, func(), error) {
	if path == "" {
		return nil, func() {}, nil
	}
	w, err := sync.NewJSONLWriter(path)
	if err != nil {
		return nil, func() {}, err
	}
	s.log.Warn("el archivo de salida va a contener datos reales de negocio: tratalo como dato sensible",
		slog.String("out", path))
	closeFn := func() {
		if err := w.Close(); err != nil {
			fmt.Fprintln(stderr, "error cerrando la salida:", err)
		}
	}
	return w.Write, closeFn, nil
}

func runSyncSales(args []string) error {
	fs := flag.NewFlagSet("sync-sales", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cf := bindCommon(fs)
	out := fs.String("out", "", "archivo JSONL de inspeccion (opt-in). CONTIENE DATOS REALES: clientes, vendedores, precios")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "Uso: mylos-tango-agent sync-sales --from DD/MM/AAAA --to DD/MM/AAAA [opciones]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	s, err := newSession(cf)
	if err != nil {
		return err
	}
	defer s.stop()

	processID, err := s.cfg.SalesProcessID()
	if err != nil {
		return err
	}
	sink, closeSink, err := s.openSink(*out)
	if err != nil {
		return err
	}
	defer closeSink()

	return s.ventas(processID, cf, sink, *out)
}

func runSyncCustomers(args []string) error {
	fs := flag.NewFlagSet("sync-customers", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cf := bindCommon(fs)
	out := fs.String("out", "", "archivo JSONL de inspeccion (opt-in). CONTIENE DATOS REALES de clientes")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "Uso: mylos-tango-agent sync-customers --from DD/MM/AAAA --to DD/MM/AAAA [opciones]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	s, err := newSession(cf)
	if err != nil {
		return err
	}
	defer s.stop()

	processID, err := s.cfg.CustomersProcessID()
	if err != nil {
		return err
	}
	sink, closeSink, err := s.openSink(*out)
	if err != nil {
		return err
	}
	defer closeSink()

	return s.clientes(processID, cf, sink, *out)
}

func runSync(args []string) error {
	fs := flag.NewFlagSet("sync", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cf := bindCommon(fs)
	outDir := fs.String("out-dir", "", "directorio donde escribir clientes.jsonl y ventas.jsonl (opt-in). CONTIENEN DATOS REALES")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "Uso: mylos-tango-agent sync --from DD/MM/AAAA --to DD/MM/AAAA [opciones]")
		fmt.Fprintln(stderr, "Corre las dos consultas en secuencia: primero clientes, despues ventas.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	s, err := newSession(cf)
	if err != nil {
		return err
	}
	defer s.stop()

	// Los dos process ids se exigen ANTES de salir a la red: si falta alguno,
	// el comando falla entero y de una, no a mitad de camino.
	customersID, errC := s.cfg.CustomersProcessID()
	salesID, errS := s.cfg.SalesProcessID()
	if err := errors.Join(errC, errS); err != nil {
		return fmt.Errorf("sync necesita los dos process ids configurados:\n%w", err)
	}

	var outCustomers, outSales string
	if *outDir != "" {
		outCustomers = filepath.Join(*outDir, "clientes.jsonl")
		outSales = filepath.Join(*outDir, "ventas.jsonl")
	}

	// Secuencial y a proposito: primero clientes, despues ventas.
	if err := s.etapaClientes(customersID, cf, outCustomers); err != nil {
		return fmt.Errorf("sync: fallo la etapa de clientes, no se corrieron las ventas: %w", err)
	}
	if err := s.etapaVentas(salesID, cf, outSales); err != nil {
		return fmt.Errorf("sync: los clientes se leyeron bien pero fallo la etapa de ventas: %w", err)
	}
	return nil
}

// etapaClientes / etapaVentas envuelven una etapa de `sync` con su sink propio,
// de modo que el archivo se cierre antes de arrancar la etapa siguiente.
func (s *session) etapaClientes(processID string, cf *commonFlags, out string) error {
	sink, closeSink, err := s.openSink(out)
	if err != nil {
		return err
	}
	defer closeSink()
	return s.clientes(processID, cf, sink, out)
}

func (s *session) etapaVentas(processID string, cf *commonFlags, out string) error {
	sink, closeSink, err := s.openSink(out)
	if err != nil {
		return err
	}
	defer closeSink()
	return s.ventas(processID, cf, sink, out)
}

func (s *session) clientes(processID string, cf *commonFlags, sink sync.Sink, out string) error {
	opts := s.options(processID, cf)
	s.log.Info("leyendo clientes",
		slog.String("process", opts.ProcessID),
		slog.String("from_date", opts.FromDate),
		slog.String("to_date", opts.ToDate),
		slog.Int("page_size", opts.PageSize),
	)

	summary, err := sync.FetchCustomers(s.ctx, s.client, opts, s.log, sink)
	if err != nil {
		if summary.Pages > 0 {
			printCustomersSummary(summary, opts, out)
		}
		return err
	}
	printCustomersSummary(summary, opts, out)
	return nil
}

func (s *session) ventas(processID string, cf *commonFlags, sink sync.Sink, out string) error {
	opts := s.options(processID, cf)
	s.log.Info("leyendo ventas",
		slog.String("process", opts.ProcessID),
		slog.String("from_date", opts.FromDate),
		slog.String("to_date", opts.ToDate),
		slog.Int("page_size", opts.PageSize),
	)

	summary, err := sync.FetchSales(s.ctx, s.client, opts, s.log, sink)
	if summary.Pages > 0 || err == nil {
		printSalesSummary(summary, opts, out)
	}
	// Diagnostico del unknown abierto: si las fechas que devolvio Tango caen
	// fuera del rango pedido, el filtro no hace lo que asumimos.
	if msg := rangoSospechoso(summary.MinFecha, summary.MaxFecha, s.from, s.to); msg != "" {
		fmt.Fprintln(stderr, msg)
	}
	return err
}

func printSalesSummary(s sync.SalesSummary, opts sync.Options, out string) {
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "== Resumen ventas ==")
	fmt.Fprintf(stdout, "  process            : %s\n", opts.ProcessID)
	fmt.Fprintf(stdout, "  rango consultado   : %s -> %s\n", opts.FromDate, opts.ToDate)
	fmt.Fprintf(stdout, "  paginas leidas     : %d\n", s.Pages)
	fmt.Fprintf(stdout, "  filas (renglones)  : %d\n", s.Rows)
	// FECHA_DE_EMISION en orden de aparicion, no ordenada.
	fmt.Fprintf(stdout, "  1ra fecha emision  : %s\n", orGuion(s.PrimeraFecha))
	fmt.Fprintf(stdout, "  ult fecha emision  : %s\n", orGuion(s.UltimaFecha))
	if s.MinFecha != s.PrimeraFecha || s.MaxFecha != s.UltimaFecha {
		fmt.Fprintf(stdout, "  min/max observados : %s -> %s (el resultado no vino ordenado)\n", s.MinFecha, s.MaxFecha)
	}
	fmt.Fprintf(stdout, "  totalCount Tango   : %d (totalPages %d)\n", s.TotalCountReported, s.TotalPagesReported)
	fmt.Fprintf(stdout, "  comprobantes       : %d\n", s.Comprobantes)
	if len(s.RowsPorTipo) > 0 {
		var parts []string
		for _, t := range s.TiposOrdenados() {
			parts = append(parts, fmt.Sprintf("%s=%d", t, s.RowsPorTipo[t]))
		}
		fmt.Fprintf(stdout, "  filas por tipo     : %s\n", strings.Join(parts, " "))
	}
	if s.AmountsComputed {
		fmt.Fprintf(stdout, "  suma CANTIDAD      : %.2f\n", s.SumCantidad)
		fmt.Fprintf(stdout, "  suma TOTAL         : %.2f (orientativo: falta confirmar impuestos y signo de NC)\n", s.SumTotal)
	}
	printCola(s.Summary, out)
}

func printCustomersSummary(s sync.Summary, opts sync.Options, out string) {
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "== Resumen clientes ==")
	fmt.Fprintf(stdout, "  process            : %s\n", opts.ProcessID)
	fmt.Fprintf(stdout, "  rango consultado   : %s -> %s\n", opts.FromDate, opts.ToDate)
	fmt.Fprintf(stdout, "  paginas leidas     : %d\n", s.Pages)
	fmt.Fprintf(stdout, "  filas              : %d\n", s.Rows)
	fmt.Fprintf(stdout, "  totalCount Tango   : %d (totalPages %d)\n", s.TotalCountReported, s.TotalPagesReported)
	printCola(s, out)
}

// printCola imprime lo comun al final de cualquier resumen.
func printCola(s sync.Summary, out string) {
	fmt.Fprintf(stdout, "  duracion           : %s\n", s.Elapsed.Round(time.Millisecond))
	if out != "" {
		fmt.Fprintf(stdout, "  salida JSONL       : %s\n", out)
	}
	if s.Truncated {
		fmt.Fprintln(stdout, "  AVISO: corte por --max-pages, el resumen es parcial")
	}
	if s.TotalCountReported > 0 && !s.Truncated && s.Rows != s.TotalCountReported {
		fmt.Fprintf(stdout, "  AVISO: filas leidas (%d) != totalCount informado (%d).\n", s.Rows, s.TotalCountReported)
		fmt.Fprintln(stdout, "         Puede ser actividad en Tango durante la paginacion (ver README).")
	}
	fmt.Fprintln(stdout)
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
		return slog.New(slog.NewJSONHandler(stderr, opts))
	}
	return slog.New(slog.NewTextHandler(stderr, opts))
}
