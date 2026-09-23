package sync

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/mylos/mylos-tango-agent/internal/model"
	"github.com/mylos/mylos-tango-agent/internal/tango"
)

// FetchCustomers recorre la consulta Live de clientes. Mismo endpoint y misma
// paginacion que ventas: lo unico que cambia es Options.ProcessID.
//
// No modelamos ningun campo todavia. Las filas se entregan al sink como el
// JSON original que mando Tango (model.RawRow), asi que no se pierde nada
// aunque la consulta tenga columnas que no conocemos. El resumen, por lo
// tanto, cuenta paginas y filas y nada mas: cualquier agregado seria una
// suposicion sobre columnas que todavia no vimos.
func FetchCustomers(ctx context.Context, c *tango.Client, opts Options, log *slog.Logger, sink Sink) (Summary, error) {
	return paginate(ctx, c, opts, log, func(rows []model.RawRow) error {
		if sink == nil {
			return nil
		}
		raws := make([]json.RawMessage, 0, len(rows))
		for _, row := range rows {
			raws = append(raws, row.Raw)
		}
		return sink(raws)
	})
}
