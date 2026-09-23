package model

import "encoding/json"

// RawRow es una fila de la que todavia no modelamos ningun campo: guarda el
// JSON original intacto y nada mas.
//
// Se usa para la consulta de clientes (process configurable). No sabemos que
// columnas trae ni como se van a llamar, y cerrar un struct a ciegas garantiza
// perder datos en silencio. Cuando veamos un JSONL real y las columnas esten
// estables, se tipea lo que haga falta, igual que se hizo con SalesLine.
type RawRow struct {
	Raw json.RawMessage
}

// UnmarshalJSON se queda con los bytes tal cual vinieron.
func (r *RawRow) UnmarshalJSON(b []byte) error {
	r.Raw = append(json.RawMessage(nil), b...)
	return nil
}
