package sync

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"

	"github.com/mylos/mylos-tango-agent/internal/model"
)

// JSONLWriter vuelca una fila por linea, tal cual la mando Tango.
// Es solo para inspeccion manual: no es un formato de intercambio.
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

// Write implementa Sink.
func (j *JSONLWriter) Write(row model.SalesLine) error {
	line := row.Raw
	if len(line) == 0 {
		b, err := json.Marshal(row)
		if err != nil {
			return err
		}
		line = b
	}
	if _, err := j.w.Write(line); err != nil {
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
