// Package sync recorre una consulta Live de Tango pagina por pagina y entrega
// las filas a un sink. FASE 1: solo lee Tango y resume. No habla con MYLOS.
package sync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/mylos/mylos-tango-agent/internal/model"
	"github.com/mylos/mylos-tango-agent/internal/tango"
)

// Options define que se pide y hasta donde.
type Options struct {
	ProcessID   string
	FromDate    string // ya formateado como lo espera Tango
	ToDate      string
	PageSize    int
	MaxPages    int // 0 = sin limite
	CustomQuery string

	// SumAmounts habilita el acumulado de TOTAL y CANTIDAD. Apagado por
	// defecto: todavia no sabemos si TOTAL incluye impuestos ni si las notas
	// de credito vienen en negativo, asi que la suma seria un numero con
	// significado incierto. Se prende explicitamente para explorar.
	SumAmounts bool
}

// Summary es el resultado agregado de una corrida.
type Summary struct {
	Pages              int
	Rows               int
	TotalCountReported int // totalCount que informo Tango en la primera pagina
	TotalPagesReported int
	Comprobantes       int // NRO_COMPROBANTE distintos (tipo+nro)
	RowsPorTipo        map[string]int

	// Fechas en orden de aparicion: la primera y la ultima fila que devolvio
	// Tango. Si difieren de Min/Max, el resultado no vino ordenado.
	PrimeraFecha string
	UltimaFecha  string
	MinFecha     string
	MaxFecha     string

	// Validos solo si AmountsComputed es true (Options.SumAmounts).
	AmountsComputed bool
	SumTotal        float64
	SumCantidad     float64

	Truncated bool // se corto por MaxPages
	Elapsed   time.Duration
}

// Sink recibe cada fila. Puede ser nil si solo interesa el resumen.
type Sink func(model.SalesLine) error

// FetchSales recorre todas las paginas hasta hasNextPage=false.
//
// RIESGO CONOCIDO, NO RESUELTO: la paginacion no es una foto consistente. Si
// alguien factura, anula o modifica un comprobante del rango mientras estamos
// paginando, el server recalcula el offset entre requests y podemos leer una
// fila dos veces o saltearnos una. Hoy solo lo detectamos a posteriori
// (Rows != TotalCountReported) y lo reportamos. La solucion va a ser
// idempotencia del lado de MYLOS mas ventanas de tiempo solapadas; se diseña
// cuando exista el contrato, no antes.
func FetchSales(ctx context.Context, c *tango.Client, opts Options, log *slog.Logger, sink Sink) (Summary, error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if opts.PageSize <= 0 {
		return Summary{}, errors.New("sync: pageSize debe ser > 0")
	}

	start := time.Now()
	sum := Summary{RowsPorTipo: map[string]int{}, AmountsComputed: opts.SumAmounts}
	seen := map[string]struct{}{}

	for pageIndex := 0; ; pageIndex++ {
		if opts.MaxPages > 0 && sum.Pages >= opts.MaxPages {
			sum.Truncated = true
			log.Warn("sync: corte por --max-pages", slog.Int("max_pages", opts.MaxPages))
			break
		}

		page, err := tango.GetApiLiveQueryData[model.SalesLine](ctx, c, tango.QueryParams{
			Process:     opts.ProcessID,
			FromDate:    opts.FromDate,
			ToDate:      opts.ToDate,
			PageSize:    opts.PageSize,
			PageIndex:   pageIndex,
			CustomQuery: opts.CustomQuery,
		})
		if err != nil {
			return sum, fmt.Errorf("sync: fallo la pagina %d: %w", pageIndex, err)
		}

		if pageIndex == 0 {
			sum.TotalCountReported = page.TotalCount
			sum.TotalPagesReported = page.TotalPages
		}
		if page.PageIndex != pageIndex {
			// No abortamos: dejamos rastro por si el server ignora pageIndex.
			log.Warn("sync: pageIndex devuelto distinto al pedido",
				slog.Int("pedido", pageIndex),
				slog.Int("devuelto", page.PageIndex),
			)
		}

		sum.Pages++
		for _, row := range page.List {
			sum.Rows++
			if opts.SumAmounts {
				sum.SumTotal += row.Total
				sum.SumCantidad += row.Cantidad
			}
			sum.RowsPorTipo[row.TipoComprobante]++
			key := row.TipoComprobante + "|" + row.NroComprobante
			if _, ok := seen[key]; !ok {
				seen[key] = struct{}{}
				sum.Comprobantes++
			}
			if row.FechaDeEmision != "" {
				if sum.PrimeraFecha == "" {
					sum.PrimeraFecha = row.FechaDeEmision
				}
				sum.UltimaFecha = row.FechaDeEmision
				// Comparacion lexicografica: sirve porque el formato es
				// ISO de ancho fijo ("2026-01-02T00:00:00").
				if sum.MinFecha == "" || row.FechaDeEmision < sum.MinFecha {
					sum.MinFecha = row.FechaDeEmision
				}
				if row.FechaDeEmision > sum.MaxFecha {
					sum.MaxFecha = row.FechaDeEmision
				}
			}
			if sink != nil {
				if err := sink(row); err != nil {
					return sum, fmt.Errorf("sync: fallo escribiendo la salida: %w", err)
				}
			}
		}

		log.Info("sync: pagina leida",
			slog.Int("page_index", pageIndex),
			slog.Int("rows", len(page.List)),
			slog.Int("rows_acumuladas", sum.Rows),
			slog.Int("total_count", page.TotalCount),
			slog.Bool("has_next_page", page.HasNextPage),
		)

		if !page.HasNextPage {
			break
		}
		if len(page.List) == 0 {
			// Guarda contra un hasNextPage=true eterno con lista vacia.
			log.Warn("sync: hasNextPage=true pero la pagina vino vacia; se corta")
			break
		}
	}

	sum.Elapsed = time.Since(start)
	return sum, nil
}

// TiposOrdenados devuelve los tipos de comprobante ordenados alfabeticamente,
// para imprimir el resumen de forma estable.
func (s Summary) TiposOrdenados() []string {
	out := make([]string, 0, len(s.RowsPorTipo))
	for k := range s.RowsPorTipo {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
