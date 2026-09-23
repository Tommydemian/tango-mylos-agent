package sync

import (
	"context"
	"log/slog"
	"sort"

	"github.com/mylos/mylos-tango-agent/internal/model"
	"github.com/mylos/mylos-tango-agent/internal/tango"
)

// SalesSummary agrega al resumen comun lo que solo tiene sentido en ventas.
type SalesSummary struct {
	Summary

	Comprobantes int // NRO_COMPROBANTE distintos (tipo+nro)
	RowsPorTipo  map[string]int

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
}

// FetchSales recorre la consulta Live de ventas. El process sale de
// Options.ProcessID, que el comando toma de la config: nunca esta hardcodeado.
//
// La consulta devuelve una fila por renglon, asi que Rows > Comprobantes.
func FetchSales(ctx context.Context, c *tango.Client, opts Options, log *slog.Logger, sink Sink) (SalesSummary, error) {
	s := SalesSummary{
		RowsPorTipo:     map[string]int{},
		AmountsComputed: opts.SumAmounts,
	}
	seen := map[string]struct{}{}

	base, err := paginate(ctx, c, opts, log, func(row model.SalesLine) error {
		if opts.SumAmounts {
			s.SumTotal += row.Total
			s.SumCantidad += row.Cantidad
		}
		s.RowsPorTipo[row.TipoComprobante]++

		key := row.TipoComprobante + "|" + row.NroComprobante
		if _, ok := seen[key]; !ok {
			seen[key] = struct{}{}
			s.Comprobantes++
		}

		if row.FechaDeEmision != "" {
			if s.PrimeraFecha == "" {
				s.PrimeraFecha = row.FechaDeEmision
			}
			s.UltimaFecha = row.FechaDeEmision
			// Comparacion lexicografica: sirve porque el formato es ISO de
			// ancho fijo ("2026-01-02T00:00:00").
			if s.MinFecha == "" || row.FechaDeEmision < s.MinFecha {
				s.MinFecha = row.FechaDeEmision
			}
			if row.FechaDeEmision > s.MaxFecha {
				s.MaxFecha = row.FechaDeEmision
			}
		}

		if sink != nil {
			return sink(row.Raw)
		}
		return nil
	})

	// El resumen parcial sirve para diagnosticar aunque haya fallado.
	s.Summary = base
	return s, err
}

// TiposOrdenados devuelve los tipos de comprobante ordenados alfabeticamente,
// para imprimir el resumen de forma estable.
func (s SalesSummary) TiposOrdenados() []string {
	out := make([]string, 0, len(s.RowsPorTipo))
	for k := range s.RowsPorTipo {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
