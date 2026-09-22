package model

import "encoding/json"

// SalesLine es una fila de la consulta Live "Detalle de comprobantes"
// (process 17839). La consulta devuelve UNA FILA POR RENGLON de comprobante,
// asi que varias SalesLine pueden compartir NroComprobante.
//
// Campos confirmados empiricamente. Mas adelante la consulta va a sumar
// COD_FAMILIA / FAMILIA: cuando eso pase se agregan aca dos campos mas, y
// mientras tanto Raw ya los conserva sin perder nada.
type SalesLine struct {
	FechaDeEmision  string  `json:"FECHA_DE_EMISION"` // "2026-01-02T00:00:00" (sin zona)
	TipoComprobante string  `json:"TIPO_COMPROBANTE"`
	NroComprobante  string  `json:"NRO_COMPROBANTE"`
	NombreVendedor  string  `json:"NOMBRE_VENDEDOR"`
	RazonSocial     string  `json:"RAZON_SOCIAL"`
	CodArticulo     string  `json:"COD_ARTICULO"`
	Descripcion     string  `json:"DESCRIPCION"`
	Cantidad        float64 `json:"CANTIDAD"`
	Total           float64 `json:"TOTAL"`

	// Raw es el JSON original de la fila, tal cual lo mando Tango.
	// Sirve para el volcado JSONL de inspeccion: no se pierden campos nuevos
	// ni todavia desconocidos.
	Raw json.RawMessage `json:"-"`
}

// UnmarshalJSON decodifica los campos conocidos y ademas guarda el JSON crudo.
func (l *SalesLine) UnmarshalJSON(b []byte) error {
	type alias SalesLine
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*l = SalesLine(a)
	l.Raw = append(json.RawMessage(nil), b...)
	return nil
}
