# mylos-tango-agent

Agente Windows standalone que integra **Tango Punto de Venta on-premise** con
**MYLOS**. Go, sin dependencias, compila a un unico `.exe`.

```
Tango local :17000  --HTTP LAN-->  mylos-tango-agent.exe  --HTTPS outbound-->  MYLOS API
GET /Api/GetApiLiveQueryData                    POST /integrations/tango/{customers,sales}/batch
```

Regla de arquitectura: **MYLOS cloud nunca se conecta directo a Tango.** El agente
corre adentro de la red del cliente y siempre inicia conexiones hacia afuera.

## Contexto confirmado empiricamente

No inventar contratos, endpoints ni campos fuera de esto.

Servidor Tango (Perfumum):
- host LAN: `arenales_tango`
- puerto: `17000`
- Company: `2`

Consultas Tango Live con Apertura API, validadas contra el Tango real. Mismo
endpoint las dos: lo unico que cambia es el `process`.

VENTAS - "Detalle de comprobantes", process `17839`, customQuery `9`
- la custom query 9 es el schema guardado en "Mis consultas": agrega columnas
  como `COD_CLIENTE` y `DESCRIPCION_ADICIONAL` a las de siempre
- **devuelve UNA FILA POR RENGLON** de comprobante (varias filas comparten `NRO_COMPROBANTE`)
- mas adelante se le van a agregar `COD_FAMILIA` / `FAMILIA`

CLIENTES - process `17851`, customQuery `10`
- **no modelamos ninguna columna**: el payload observado trae COD_CLIENTE,
  RAZON_SOCIAL, TIPO_DE_DOCUMENTO, NUMERO, ACTIVIDAD, DOMICILIO, LOCALIDAD,
  TELEFONO, FAX, MOVIL, EMAIL, PAGINA_WEB, CONTACTO_HABITUAL,
  TELEFONO_DEL_CONTACTO, EMAIL_CONTACTO, OBS_CONTACTO, CONDICION_DE_IVA,
  DESC_RUBRO. **No asumir que esa lista sea exhaustiva ni estable.**

Los process ids y los customQuery salen de config (`TANGO_SALES_PROCESS_ID`,
`TANGO_SALES_CUSTOM_QUERY_ID`, `TANGO_CUSTOMERS_PROCESS_ID`,
`TANGO_CUSTOMERS_CUSTOM_QUERY_ID`). Nunca hardcodearlos.

Precedencia del customQuery: `--custom-query` explicito (aunque sea vacio) >
variable del dataset > vacio (el param no viaja).

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

**FASE 1: solo lector de Tango.** Tres comandos:

- `sync-sales` - consulta de ventas
- `sync-customers` - consulta de clientes
- `sync` - las dos **en secuencia: clientes primero, ventas despues**. Nunca en
  paralelo. Exige los dos process ids y falla antes de salir a la red si falta
  alguno. Si la etapa de clientes falla, no corre la de ventas.

Todos paginan hasta `hasNextPage=false`, validan `succeeded`, resumen y
opcionalmente escriben JSONL (`--out`, o `--out-dir` en `sync`).

**El agente es transporte y nada mas.** No mapea, no transforma, no interpreta
campos (`DESCRIPCION_ADICIONAL` incluido), no deduplica semanticamente. Las
filas van a MYLOS tal cual salieron de Tango.

Ingesta MYLOS (backend ya en prod):
- `POST /integrations/tango/customers/batch` y `.../sales/batch`
- `Authorization: Bearer <MYLOS_INGEST_TOKEN>`; el backend resuelve el tenant
  por el token, el body NO lleva tenant_id
- body: `company_id` (num), `process_id` (num), `custom_query_id` (string),
  `from_date`, `to_date`, `rows` (raw de Tango)
- respuesta: `{batch_id, received, stored, duplicate}`
- **`duplicate=true` + `stored=false` es EXITO**, no error: idempotencia por
  payload_hash del lado del backend
- **un batch por pagina de Tango**, para no juntar el dataset en memoria
- no implementar idempotencia local: ya la resuelve el backend

No existen (a proposito, no son deuda): Windows Service, base de datos local,
scheduler, colas, observabilidad. Se agregan cuando el problema exista.

## Estructura

```
cmd/agent/          CLI (flags, subcomandos, resumenes)
internal/config/    config por env/archivo + validacion. Config.LogValue() redacta el token
                    SalesProcessID() / CustomersProcessID(): cada comando pide el que usa
internal/tango/     cliente HTTP: GetApiLiveQueryData[T], retry/backoff, errores tipados
internal/mylos/     cliente HTTP de ingesta + Uploader (batch por pagina, stats)
                    retry propio, duplicado del criterio de tango: si cambia uno, revisar el otro
internal/sync/      paginate[T]() generico (el loop de paginas, uno solo para todos)
                    FetchSales -> SalesSummary; FetchCustomers -> Summary; JSONLWriter
internal/model/     Envelope[T] / Page[T] / SalesLine / RawRow

Agregar una consulta Live nueva = un process id en config + una FetchX que
llame a paginate. No hace falta tocar el cliente HTTP ni el paginador.
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
- **No degradar un unknown a hecho por conveniencia.** Si algo no se demostro
  contra el server real, en el codigo y en los docs va como hipotesis. Vale
  tambien para lo que se afirme en una conversacion.
- **No inventar agregados con semantica incierta.** Las sumas de `TOTAL` /
  `CANTIDAD` estan detras de `--sum-amounts` hasta que sepamos que representa
  `TOTAL`. Un numero dudoso en un resumen se lee como verdad.
- **No agregar campos al modelo que no vinieron del server.** Si Tango suma
  `COD_FAMILIA`/`FAMILIA`, primero verlos en un JSONL real y despues tipearlos.
  Mientras tanto `SalesLine.Raw` ya los conserva.
- **El JSONL siempre guarda el JSON original, no el struct.** El schema de una
  custom query puede cambiar sin avisar; el volcado tiene que reflejar lo que
  mando Tango, no lo que el agente entiende.
- **Clientes va como `RawRow` a proposito.** No cerrar un struct de clientes
  hasta haber visto un JSONL real: un struct a ciegas descarta columnas en
  silencio. El sink recibe `json.RawMessage`, no filas tipadas.
- **Tests sin Tango real**: todo con `httptest`. `go test ./...` tiene que pasar
  offline.
- **Reintentar un POST solo es seguro por la idempotencia del backend.** Si eso
  cambiara, hay que revisar la politica de retry de `internal/mylos`.
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
2. **Semantica del filtro de fechas: SIN DEMOSTRAR.** El default
   `02/01/2006` (`dd/MM/yyyy`) es una hipotesis, no un hecho. La unica
   respuesta real que tenemos muestra `"FECHA_DE_EMISION": "2026-01-02T00:00:00"`
   para lo que se creia un rango de septiembre. Esa fila puede venir de otra
   corrida, de un filtro que no se aplico, o de una semantica del endpoint que
   no entendemos. **No tratar dd/MM/yyyy como confirmado hasta la prueba de
   la seccion siguiente.** Tampoco asumir que 0 filas = formato incorrecto.
3. Forma de `exceptionInfo` cuando viene poblado: solo lo vimos `null`.
4. Zona horaria / semantica de `FECHA_DE_EMISION` (viene sin offset).
5. Si `TOTAL` incluye impuestos y si las notas de credito vienen en negativo.
   Por eso las sumas estan detras de `--sum-amounts`.
6. Contrato MYLOS: inexistente.
