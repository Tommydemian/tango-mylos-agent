package mylos

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"
)

// Meta son los datos del batch que no cambian entre paginas.
type Meta struct {
	CompanyID     int
	ProcessID     int
	CustomQueryID string
	FromDate      string
	ToDate        string
}

// Stats es lo que se acumula a lo largo de una etapa.
type Stats struct {
	Batches     int // batches enviados (paginas no vacias)
	Rows        int // filas enviadas
	RowsStored  int // filas almacenadas por MYLOS (suma de stored)
	Duplicates  int // batches que MYLOS descarto por duplicados
	LastBatchID int64
	Elapsed     time.Duration
}

// Uploader manda una pagina de Tango por batch al endpoint de un dataset.
//
// Un batch por pagina, a proposito: asi el agente nunca junta el dataset
// entero en memoria y el tamanio del envio lo controla --page-size.
type Uploader struct {
	client  *Client
	dataset Dataset
	meta    Meta
	log     *slog.Logger
	stats   Stats
}

// NewUploader arma el uploader de un dataset.
func NewUploader(c *Client, d Dataset, meta Meta, log *slog.Logger) *Uploader {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Uploader{client: c, dataset: d, meta: meta, log: log}
}

// SendPage manda una pagina como batch. Sirve como sync.Sink.
//
// Las filas van tal cual vinieron de Tango: el agente no las toca.
func (u *Uploader) SendPage(ctx context.Context, rows []json.RawMessage) error {
	if len(rows) == 0 {
		// Una pagina vacia no genera batch: no hay nada que ingestar.
		return nil
	}
	start := time.Now()

	resp, err := u.client.SendBatch(ctx, u.dataset, Batch{
		CompanyID:     u.meta.CompanyID,
		ProcessID:     u.meta.ProcessID,
		CustomQueryID: u.meta.CustomQueryID,
		FromDate:      u.meta.FromDate,
		ToDate:        u.meta.ToDate,
		Rows:          rows,
	})
	elapsed := time.Since(start)
	u.stats.Elapsed += elapsed
	if err != nil {
		return fmt.Errorf("mylos: fallo el envio del batch de %s: %w", u.dataset, err)
	}

	u.stats.Batches++
	u.stats.Rows += len(rows)
	u.stats.LastBatchID = resp.BatchID
	u.stats.RowsStored += resp.Stored
	// duplicate=true es exito: el backend ya tenia este batch y lo descarto,
	// asi que stored viene en 0.
	if resp.Duplicate {
		u.stats.Duplicates++
	}

	u.log.Info("mylos: batch enviado",
		slog.String("dataset", string(u.dataset)),
		slog.String("target", u.client.Target(u.dataset)),
		slog.Int("rows_enviadas", len(rows)),
		slog.Int64("batch_id", resp.BatchID),
		slog.Int("received", resp.Received),
		slog.Int("stored", resp.Stored),
		slog.Bool("duplicate", resp.Duplicate),
		slog.Duration("duracion", elapsed.Round(time.Millisecond)),
	)
	if resp.Received != len(rows) {
		u.log.Warn("mylos: received no coincide con las filas enviadas",
			slog.Int("enviadas", len(rows)),
			slog.Int("received", resp.Received),
			slog.Int64("batch_id", resp.BatchID),
		)
	}
	return nil
}

// Stats devuelve el acumulado de la etapa.
func (u *Uploader) Stats() Stats { return u.stats }
