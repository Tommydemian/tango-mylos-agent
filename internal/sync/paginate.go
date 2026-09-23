// Package sync recorre una consulta Live de Tango pagina por pagina y entrega
// las filas a un sink. FASE 1: solo lee Tango y resume. No habla con MYLOS.
package sync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/mylos/mylos-tango-agent/internal/tango"
)

// Options define que consulta se pide y hasta donde.
type Options struct {
	ProcessID   string // el unico dato que cambia entre ventas y clientes
	FromDate    string // ya formateado como lo espera Tango
	ToDate      string
	PageSize    int
	MaxPages    int // 0 = sin limite
	CustomQuery string

	// SumAmounts habilita el acumulado de TOTAL y CANTIDAD (solo ventas).
	// Apagado por defecto: todavia no sabemos si TOTAL incluye impuestos ni si
	// las notas de credito vienen en negativo, asi que la suma seria un numero
	// con significado incierto. Se prende explicitamente para explorar.
	SumAmounts bool
}

// Sink recibe las filas de UNA pagina de Tango, con su JSON original.
//
// Es raw a proposito: lo que se vuelca a disco o se manda a MYLOS tiene que
// ser exactamente lo que mando Tango, sin pasar por ningun struct que pueda
// descartar campos.
//
// Trabaja de a paginas, no de a filas, para que el agente nunca junte el
// dataset entero en memoria: cada pagina se procesa y se descarta.
type Sink func(rows []json.RawMessage) error

// Sinks encadena varios sinks sobre la misma pagina, en orden.
// Si uno falla, los siguientes no corren y el error sube.
func Sinks(ss ...Sink) Sink {
	activos := make([]Sink, 0, len(ss))
	for _, s := range ss {
		if s != nil {
			activos = append(activos, s)
		}
	}
	if len(activos) == 0 {
		return nil
	}
	if len(activos) == 1 {
		return activos[0]
	}
	return func(rows []json.RawMessage) error {
		for _, s := range activos {
			if err := s(rows); err != nil {
				return err
			}
		}
		return nil
	}
}

// Summary es lo que toda corrida sabe informar, sea de la consulta que sea.
type Summary struct {
	Pages              int
	Rows               int
	TotalCountReported int // totalCount que informo Tango en la primera pagina
	TotalPagesReported int
	Truncated          bool // se corto por MaxPages
	Elapsed            time.Duration
}

// paginate recorre las paginas de una consulta Live desde pageIndex=0 hasta
// hasNextPage=false, decodificando cada fila en T y pasandola por onRow.
//
// RIESGO CONOCIDO, NO RESUELTO: la paginacion no es una foto consistente. Si
// alguien factura, anula o modifica algo del rango mientras estamos paginando,
// el server recalcula el offset entre requests y podemos leer una fila dos
// veces o saltearnos una. Hoy solo lo detectamos a posteriori
// (Rows != TotalCountReported) y lo reportamos. La solucion va a ser
// idempotencia del lado de MYLOS mas ventanas de tiempo solapadas; se disenia
// cuando exista el contrato, no antes.
func paginate[T any](ctx context.Context, c *tango.Client, opts Options, log *slog.Logger, onPage func(rows []T) error) (Summary, error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if opts.PageSize <= 0 {
		return Summary{}, errors.New("sync: pageSize debe ser > 0")
	}

	start := time.Now()
	var sum Summary

	for pageIndex := 0; ; pageIndex++ {
		if opts.MaxPages > 0 && sum.Pages >= opts.MaxPages {
			sum.Truncated = true
			log.Warn("sync: corte por --max-pages", slog.Int("max_pages", opts.MaxPages))
			break
		}

		page, err := tango.GetApiLiveQueryData[T](ctx, c, tango.QueryParams{
			Process:     opts.ProcessID,
			FromDate:    opts.FromDate,
			ToDate:      opts.ToDate,
			PageSize:    opts.PageSize,
			PageIndex:   pageIndex,
			CustomQuery: opts.CustomQuery,
		})
		if err != nil {
			sum.Elapsed = time.Since(start)
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
		sum.Rows += len(page.List)
		if err := onPage(page.List); err != nil {
			sum.Elapsed = time.Since(start)
			return sum, err
		}

		log.Info("sync: pagina leida",
			slog.String("process", opts.ProcessID),
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
