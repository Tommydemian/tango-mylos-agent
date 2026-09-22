# mylos-tango-agent

Agente Windows standalone que integra **Tango Punto de Venta on-premise** con
**MYLOS**. Go, sin dependencias, compila a un unico `.exe`.

```
Tango local :17000  --HTTP LAN-->  mylos-tango-agent.exe  --HTTPS outbound-->  MYLOS API
```

Regla de arquitectura: **MYLOS cloud nunca se conecta directo a Tango.** El agente
corre adentro de la red del cliente y siempre inicia conexiones hacia afuera.

## Contexto confirmado empiricamente

No inventar contratos, endpoints ni campos fuera de esto.

Servidor Tango (Perfumum):
- host LAN: `arenales_tango`
- puerto: `17000`
- Company: `2`

Consulta Tango Live con Apertura API:
- nombre: "Detalle de comprobantes"
- process id: `17839`
- **devuelve UNA FILA POR RENGLON** de comprobante (varias filas comparten `NRO_COMPROBANTE`)
- mas adelante se le van a agregar `COD_FAMILIA` / `FAMILIA`

Endpoint:

```
GET http://<host>:17000/Api/GetApiLiveQueryData

Headers:
  Accept: application/json
  ApiAuthorization: <TOKEN>     <- SECRETO, nunca loguear
  Company: <COMPANY_ID>

Params:
  process, fromDate, toDate, pageSize, pageIndex, customQuery (opcional)

pageIndex arranca en 0.
```

Respuesta real confirmada:

```json
{
  "resultData": {
    "list": [
      {
        "FECHA_DE_EMISION": "2026-01-02T00:00:00",
        "TIPO_COMPROBANTE": "FAC",
        "NRO_COMPROBANTE": "B0001500020858",
        "NOMBRE_VENDEDOR": "GABRIELA CONTARTESE",
        "RAZON_SOCIAL": "SACO NATALIA",
        "COD_ARTICULO": "PAT015-029",
        "DESCRIPCION": "TEXTIL VERBENA 1,5L",
        "CANTIDAD": 1,
        "TOTAL": 48760.330579
      }
    ],
    "pageIndex": 0,
    "pageSize": 10,
    "totalCount": 21812,
    "totalPages": 2182,
    "hasPreviousPage": false,
    "hasNextPage": true
  },
  "message": null,
  "exceptionInfo": null,
  "succeeded": true
}
```

## Estado

**FASE 1 terminada: solo lector de Tango.** `sync-sales` lee un rango, pagina
hasta `hasNextPage=false`, valida `succeeded`, resume y opcionalmente escribe JSONL.

**NO existe integracion con MYLOS** y no hay que inventarla. Cuando aparezca el
contrato real se agrega `internal/mylos/`.

Tampoco existen (a proposito, no son deuda): Windows Service, base de datos local,
scheduler, colas, observabilidad. Se agregan cuando el problema exista.

## Estructura

```
cmd/agent/          CLI (flags, subcomandos, resumen)
internal/config/    config por env/archivo + validacion. Config.LogValue() redacta el token
internal/tango/     cliente HTTP: GetApiLiveQueryData[T], retry/backoff, errores tipados
internal/sync/      recorrido de paginas, Summary, JSONLWriter
internal/model/     Envelope[T] / Page[T] / SalesLine, calcados de la respuesta real
```

Errores tipados en `internal/tango/errors.go`: `HTTPError` (status no-2xx),
`APIError` (`succeeded=false`), `ResponseError` (JSON o forma inesperada).
Se reintenta transporte, 5xx, 429 y 408. No se reintenta el resto de 4xx,
`succeeded=false` ni JSON invalido.

## Reglas al trabajar en este repo

- **Nunca loguear ni meter en un error el `ApiAuthorization`.** Hay tests que lo
  verifican; si agregas un camino de error nuevo, cubrilo.
- **No hardcodear secretos ni hosts de clientes.** Todo por env. `.env` no se commitea.
- **`--out` es opt-in y siempre lo va a ser.** El JSONL tiene PII y datos
  comerciales reales (razon social, vendedor, articulos, importes). No lo
  actives por defecto, no lo dejes en rutas compartidas, no lo commitees.
- **No inventar agregados con semantica incierta.** Las sumas de `TOTAL` /
  `CANTIDAD` estan detras de `--sum-amounts` hasta que sepamos que representa
  `TOTAL`. Un numero dudoso en un resumen se lee como verdad.
- **No agregar campos al modelo que no vinieron del server.** Si Tango suma
  `COD_FAMILIA`/`FAMILIA`, primero verlos en un JSONL real y despues tipearlos.
  Mientras tanto `SalesLine.Raw` ya los conserva.
- **Tests sin Tango real**: todo con `httptest`. `go test ./...` tiene que pasar
  offline.
- **Sin dependencias externas.** No tener `go.sum` es una feature: el `.exe` se
  audita facil y se compila en cualquier lado.
- No abstraer por adelantado. Si hay una sola implementacion, no hay interfaz.

## Comandos

```bash
make test             # tests
make race             # tests con -race
make build            # binario Linux
make build-windows    # bin/mylos-tango-agent.exe
```

```bash
./bin/mylos-tango-agent sync-sales --from 01/09/2026 --to 09/09/2026 \
  --page-size 50 --max-pages 1 --log-level debug
```

## Riesgos y unknowns abiertos

1. **Mutaciones durante la paginacion. NO resolver todavia.** N paginas son N
   requests: no es una foto consistente. Si alguien factura o anula algo del
   rango mientras paginamos, podemos duplicar o saltear una fila. Hoy solo se
   detecta a posteriori (`Rows != TotalCountReported` -> aviso en el resumen).
   Se resuelve despues, con **idempotencia en MYLOS + ventanas solapadas**,
   cuando exista el contrato. No agregar snapshots, locks ni cursores ahora.
2. Formato de `fromDate`/`toDate`: default `02/01/2006` (`dd/MM/yyyy`), que es
   lo observado en Tango. Configurable por `TANGO_DATE_FORMAT`. Falta la
   verificacion contra el server real.
3. Forma de `exceptionInfo` cuando viene poblado: solo lo vimos `null`.
4. Zona horaria / semantica de `FECHA_DE_EMISION` (viene sin offset).
5. Si `TOTAL` incluye impuestos y si las notas de credito vienen en negativo.
   Por eso las sumas estan detras de `--sum-amounts`.
6. Contrato MYLOS: inexistente.
