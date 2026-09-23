package sync

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// JSONLWriter vuelca una fila por linea, tal cual la mando Tango.
// Es solo para inspeccion manual: no es un formato de intercambio.
//
// CONTIENE DATOS REALES DE NEGOCIO (razon social, vendedor, articulos,
// importes). Tratar el archivo como dato sensible.
type JSONLWriter struct {
	f *os.File
	w *bufio.Writer
}

// NewJSONLWriter crea/trunca el archivo destino.
func NewJSONLWriter(path string) (*JSONLWriter, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("sync: no se pudo crear %s: %w", path, err)
	}
	return &JSONLWriter{f: f, w: bufio.NewWriter(f)}, nil
}

// Write implementa Sink: escribe el JSON crudo de la fila.
func (j *JSONLWriter) Write(raw json.RawMessage) error {
	if len(raw) == 0 {
		return errors.New("sync: fila sin JSON original, no se escribe nada")
	}
	if _, err := j.w.Write(raw); err != nil {
		return err
	}
	return j.w.WriteByte('\n')
}

// Close vacia el buffer y cierra el archivo.
func (j *JSONLWriter) Close() error {
	if err := j.w.Flush(); err != nil {
		j.f.Close()
		return err
	}
	return j.f.Close()
}

// JSONLWriter.Write tiene que seguir sirviendo como Sink.
var _ Sink = (*JSONLWriter)(nil).Write
