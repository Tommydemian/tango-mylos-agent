// Package model contiene los tipos que devuelve la API Live de Tango.
// Todo lo que hay aca esta calcado de una respuesta real de
// GET /Api/GetApiLiveQueryData. No agregar campos no confirmados.
package model

import "encoding/json"

// Envelope es el sobre comun de GetApiLiveQueryData.
//
//	{"resultData": {...}, "message": null, "exceptionInfo": null, "succeeded": true}
//
// ExceptionInfo se deja como RawMessage porque solo lo vimos en null:
// no conocemos su forma cuando viene poblado.
type Envelope[T any] struct {
	ResultData    *Page[T]        `json:"resultData"`
	Message       *string         `json:"message"`
	ExceptionInfo json.RawMessage `json:"exceptionInfo"`
	Succeeded     bool            `json:"succeeded"`
}

// Page es el bloque de paginado de resultData. pageIndex arranca en 0.
type Page[T any] struct {
	List            []T  `json:"list"`
	PageIndex       int  `json:"pageIndex"`
	PageSize        int  `json:"pageSize"`
	TotalCount      int  `json:"totalCount"`
	TotalPages      int  `json:"totalPages"`
	HasPreviousPage bool `json:"hasPreviousPage"`
	HasNextPage     bool `json:"hasNextPage"`
}
