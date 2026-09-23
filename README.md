# mylos-tango-agent

Agente on-premise que lee Tango Punto de Venta (LAN) y, mas adelante, empuja los
datos a MYLOS. Un unico `.exe` de Windows, sin dependencias externas ni runtime.

```
Tango local :17000
    | HTTP LAN          GET /Api/GetApiLiveQueryData
mylos-tango-agent.exe
    | HTTPS outbound    POST /integrations/tango/{customers,sales}/batch
MYLOS API
```

MYLOS cloud nunca se conecta a Tango: el agente es el unico que entra a la LAN, y
siempre sale hacia afuera.

## Estado

El agente lee una consulta Tango Live por rango de fechas, pagina hasta el
final, y **manda cada pagina a MYLOS como un batch**. Opcionalmente ademas
vuelca las filas a un JSONL para inspeccion manual.

**El agente es transporte.** No mapea, no transforma, no interpreta campos, no
deduplica. Las filas van a MYLOS tal cual salieron de Tango; el backend guarda
el raw y resuelve el negocio.

Hay dos consultas Live configuradas, ambas contra el **mismo endpoint**: lo
unico que cambia es el `process`.

| Comando | Consulta | Process (Perfumum) | customQuery |
|---|---|---|---|
| `sync-sales` | ventas, una fila por renglon de comprobante | `17839` | `9` |
| `sync-customers` | clientes | `17851` | `10` |
| `sync` | las dos, en secuencia: **clientes y despues ventas** | ambos | ambos |

Los process ids y los customQuery salen siempre de la config, nunca estan en
el codigo. El `customQuery` selecciona el schema guardado en "Mis consultas"
de Tango: el 9 agrega a ventas columnas como `COD_CLIENTE` y
`DESCRIPCION_ADICIONAL`.

No hay (a proposito): Windows Service, base local, scheduler, cliente MYLOS,
colas ni observabilidad. Se agregan cuando haya contrato real.

## Configuracion

Todo por variables de entorno. Nunca hay secretos en el codigo ni en el repo.

| Variable | Obligatoria | Default | Que es |
|---|---|---|---|
| `TANGO_BASE_URL` | si | - | `http://arenales_tango:17000` |
| `TANGO_API_TOKEN` | si | - | token de Apertura API. **Secreto.** |
| `TANGO_COMPANY_ID` | si | - | header `Company` (en Perfumum: `2`) |
| `TANGO_SALES_PROCESS_ID` | para ventas | - | process de la consulta de ventas (`17839`) |
| `TANGO_SALES_CUSTOM_QUERY_ID` | no | vacio | customQuery de ventas (`9`) |
| `TANGO_CUSTOMERS_PROCESS_ID` | para clientes | - | process de la consulta de clientes (`17851`) |
| `TANGO_CUSTOMERS_CUSTOM_QUERY_ID` | no | vacio | customQuery de clientes (`10`) |
| `MYLOS_BASE_URL` | si | - | `https://api.mylos.app` |
| `MYLOS_INGEST_TOKEN` | si | - | token de ingesta. **Secreto.** Resuelve el tenant |
| `MYLOS_HTTP_TIMEOUT` | no | `60s` | timeout por POST |
| `MYLOS_MAX_RETRIES` | no | `3` | reintentos ante errores transitorios |
| `MYLOS_RETRY_BASE_DELAY` | no | `500ms` | base del backoff |
| `TANGO_HTTP_TIMEOUT` | no | `60s` | timeout por request |
| `TANGO_MAX_RETRIES` | no | `3` | reintentos ante errores transitorios |
| `TANGO_RETRY_BASE_DELAY` | no | `500ms` | base del backoff exponencial con jitter |
| `TANGO_DATE_FORMAT` | no | `02/01/2006` | layout Go de `fromDate`/`toDate`. `dd/MM/yyyy` es **hipotesis sin demostrar**, ver *Riesgos* |

Se pueden cargar desde un archivo (`--env-file`, por defecto `.env` del directorio
actual). El entorno real siempre gana sobre el archivo.

```bash
cp .env.example .env    # completar el token; .env esta en .gitignore
```

### Precedencia del customQuery

```
--custom-query explicito   >   ..._CUSTOM_QUERY_ID del entorno   >   vacio
```

Pasarlo en la linea de comandos gana siempre, **aunque sea vacio**:
`--custom-query ""` corre la consulta con su schema por defecto aunque el
entorno defina uno. Si no se pasa el flag, se usa la variable del dataset
correspondiente. Si tampoco esta, el parametro `customQuery` no viaja en la
URL y la consulta corre igual. En `sync`, un `--custom-query` explicito aplica
a las dos etapas.

Cada comando exige solo el process que usa: `sync-sales` anda sin el de
clientes, y viceversa. `sync` exige los dos y falla **antes de salir a la red**
si falta alguno, nombrando el que falta.

## Uso

```bash
mylos-tango-agent sync-customers --from 01/09/2026 --to 09/09/2026
mylos-tango-agent sync-sales     --from 01/09/2026 --to 09/09/2026
mylos-tango-agent sync           --from 01/09/2026 --to 09/09/2026
```

`sync` corre las dos consultas **en secuencia, nunca en paralelo**: primero
clientes, despues ventas. Si falla la etapa de clientes, no corre la de ventas
y el error lo dice explicitamente.

Opciones (las mismas para los tres comandos):

| Flag | Default | Que hace |
|---|---|---|
| `--from`, `--to` | - | obligatorias. `DD/MM/AAAA` o `AAAA-MM-DD` |
| `--page-size` | `500` | filas por pagina |
| `--max-pages` | `0` | cortar tras N paginas (0 = todas). Util para probar |
| `--out` | - | **opt-in.** JSONL con una fila por renglon, tal cual la mando Tango. Solo `sync-sales` y `sync-customers`. Ver *Datos sensibles* |
| `--out-dir` | - | **opt-in.** Solo `sync`: escribe `clientes.jsonl` y `ventas.jsonl` en ese directorio |
| `--sum-amounts` | `false` | acumular `TOTAL` y `CANTIDAD` en el resumen de ventas (numero orientativo) |
| `--custom-query` | - | override del `customQuery`. Ver *Precedencia* |
| `--env-file` | `.env` | archivo de configuracion a precargar si existe |
| `--timeout` | `0` | limite para toda la corrida (ej: `10m`) |
| `--log-level` | `info` | `debug` muestra cada request |
| `--log-format` | `text` | `text` o `json` |

Ejemplo:

```bash
mylos-tango-agent sync-sales \
  --from 01/09/2026 --to 09/09/2026 \
  --page-size 500 --max-pages 1 --log-level debug
```

Salida:

```
== Resumen ventas ==
  process            : 17839
  custom query       : 9
  rango consultado   : 01/09/2026 -> 09/09/2026
  paginas leidas     : 3
  filas (renglones)  : 12
  1ra fecha emision  : 2026-09-01T00:00:00
  ult fecha emision  : 2026-09-09T00:00:00
  totalCount Tango   : 12 (totalPages 3)
  comprobantes       : 12
  filas por tipo     : FAC=9 NC=3
  duracion           : 5ms
```

`1ra`/`ult fecha emision` son la `FECHA_DE_EMISION` de la primera y la ultima
fila devueltas, **en orden de aparicion**, no ordenadas. Si el resultado no
viene ordenado por fecha, el resumen agrega una linea con el min/max observado.

El resumen **no suma importes por defecto**: no sabemos todavia si `TOTAL`
incluye impuestos ni si las notas de credito vienen en negativo, asi que un total
seria un numero de significado incierto. Con `--sum-amounts` se acumula igual,
marcado como orientativo.

La consulta devuelve **una fila por renglon**, asi que `filas` > `comprobantes`.
El log va a stderr y el resumen a stdout: se pueden separar.

Exit code `0` si termino bien, `1` si fallo.

## Como demostrar el filtro de fechas

Esta es la prueba pendiente que cierra la fase 1. Hay que elegir **un dia cuyo
numero sea mayor a 12** (asi no puede interpretarse como mes) **y del que
sepamos que hubo ventas**. Ambas condiciones son necesarias.

```bash
mylos-tango-agent sync-sales --from 19/09/2026 --to 19/09/2026 \
  --page-size 5 --max-pages 1 --log-level debug
```

Tres resultados posibles, y que significa cada uno:

| Resultado | Significa |
|---|---|
| filas con `FECHA_DE_EMISION` = `2026-09-19` | `dd/MM/yyyy` confirmado end-to-end. Unknown cerrado. |
| **0 filas** | **No concluye nada.** Puede ser un dia sin ventas. Repetir con otro dia > 12 donde haya ventas seguras. No cambiar el formato por esto. |
| filas de **otra fecha** | El filtro no se comporta como asumimos. **Parar e investigar**, no seguir construyendo encima. |

El agente avisa solo en el tercer caso: si las fechas observadas caen fuera del
rango pedido, el resumen imprime un `AVISO` explicito.

Conviene correr la prueba con **la UI de Tango cerrada**. Asi se demuestran dos
cosas de una: que el agente llega al puerto 17000, y que la API vive
independientemente de una sesion interactiva de Tango.

## Envio a MYLOS

Cada pagina de Tango se manda como **un batch**, al endpoint del dataset:

```
POST {MYLOS_BASE_URL}/integrations/tango/customers/batch
POST {MYLOS_BASE_URL}/integrations/tango/sales/batch

Authorization: Bearer <MYLOS_INGEST_TOKEN>
Content-Type: application/json

{
  "company_id": 2,
  "process_id": 17851,
  "custom_query_id": "10",
  "from_date": "22/09/2026",
  "to_date": "23/09/2026",
  "rows": [ ... tal cual vinieron de Tango ... ]
}
```

El body no lleva tenant: lo resuelve el backend por el token.

**Un batch por pagina**, no uno por corrida: el agente nunca junta el dataset
entero en memoria y `--page-size` controla el tamanio del envio. Una pagina
vacia no genera batch.

Respuesta:

```json
{"batch_id": "...", "received": 12, "stored": true, "duplicate": false}
```

`duplicate: true` con `stored: false` **es exito**: el backend ya tenia ese
batch (idempotencia por `payload_hash`) y lo descarto. El agente lo cuenta y
sigue; `sync` pasa igual a la etapa de ventas.

### Reintentos

Se reintentan errores de red, `408`, `429` y `5xx`. **No** se reintenta el
resto de `4xx` (un `401` o un `422` no mejoran insistiendo) ni una respuesta
ilegible.

Reintentar un POST es seguro *solo* porque el backend es idempotente: si el
primer intento llego pero se perdio la respuesta, el reintento vuelve con
`duplicate: true` en vez de insertar dos veces.

No hay idempotencia local ni estado en el agente: eso vive en el backend.

### Que pasa si falla a mitad de camino

Las paginas ya enviadas quedan en MYLOS. No hay rollback ni transaccion. La
forma de recuperarse es **volver a correr el mismo rango**: los batches que ya
entraron vuelven como `duplicate`. Es el mismo mecanismo que resuelve el riesgo
de mutaciones durante la paginacion.

## Compilar

Requiere Go 1.24+. Cero dependencias externas: no hay `go.sum` porque no hay que descargar nada.

```bash
make build            # binario Linux en bin/
make build-windows    # bin/mylos-tango-agent.exe (cross-compila desde Linux)
make test             # tests, no necesitan Tango real
make race             # tests con -race
```

O a mano:

```bash
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "-s -w" -o bin/mylos-tango-agent.exe ./cmd/agent
```

El `.exe` se copia al servidor Windows y se corre desde una consola; no instala
nada ni necesita permisos especiales mas alla de poder salir a la LAN.

## Estructura

```
cmd/agent/          CLI: flags, subcomandos, impresion de los resumenes
internal/config/    carga y validacion de configuracion (env / archivo)
internal/tango/     cliente HTTP de Tango: endpoint, retry, errores tipados
internal/mylos/     cliente HTTP de MYLOS + Uploader (un batch por pagina)
internal/sync/      paginate() generico + FetchSales / FetchCustomers + JSONL
internal/model/     structs calcados de la respuesta real de Tango
```

## Seguridad

- El token viaja solo en el header `ApiAuthorization`, nunca en la URL.
- Nunca se loguea: `config.Config` implementa `slog.LogValuer` y lo enmascara.
- Si el server devuelve el token dentro de un body de error, el cliente lo
  reemplaza por `[redacted]` antes de armar el error (hay test que lo verifica).
- El token de MYLOS viaja solo en `Authorization: Bearer`, nunca en la URL, y
  tampoco se loguea. Si el backend lo devuelve en un body de error, el cliente
  lo reemplaza por `[redacted]` (hay test).
- `.env` esta en `.gitignore`; `.env.example` no tiene secretos.

### Datos sensibles

`--out` y `--out-dir` son **opt-in** y nunca se activan solos. El JSONL que
generan contiene datos reales de negocio: razon social del cliente, nombre del
vendedor, articulos, cantidades e importes, y la ficha completa de clientes.
Es PII y es informacion comercial.

- No lo dejes en un directorio compartido ni lo mandes por chat/mail.
- No lo commitees: `*.jsonl` esta en `.gitignore`.
- Borralo cuando termines de inspeccionar.
- Para una prueba de conectividad no hace falta: el resumen alcanza.

## Riesgos y unknowns reales (no inventar)

1. **Mutaciones durante la paginacion (riesgo abierto, no resuelto).** Recorrer
   N paginas son N requests separados: no es una foto consistente. Si alguien
   factura, anula o modifica un comprobante del rango mientras el agente pagina,
   el server recalcula el offset entre requests y podemos **leer una fila dos
   veces o saltearnos una**. Hoy solo lo detectamos a posteriori: si
   `filas leidas != totalCount`, el resumen lo avisa. No se resuelve en fase 1
   a proposito. La solucion va a ser **idempotencia del lado de MYLOS**
   (que reprocesar la misma fila no duplique nada) mas **ventanas de tiempo
   solapadas** entre corridas, y se diseña cuando exista el contrato de MYLOS.
2. **Semantica del filtro de fechas: SIN DEMOSTRAR.** Es el unknown mas
   importante que queda abierto. El default `02/01/2006` (`dd/MM/yyyy`) es una
   **hipotesis de trabajo, no un hecho verificado**.

   La anomalia concreta: la unica respuesta real que tenemos devolvio
   `"FECHA_DE_EMISION": "2026-01-02T00:00:00"` para lo que se creia un rango de
   **septiembre**. `2026-01-02` es exactamente el string ambiguo entre
   `dd/MM` y `MM/dd`. Puede haber sido otra corrida, un filtro que no se
   aplico, o una semantica del endpoint que todavia no entendemos.

   **`0 filas` no demuestra que el formato este mal**: puede ser simplemente un
   dia sin ventas. Ver *Como demostrar el filtro de fechas*.
3. **Forma de `exceptionInfo`**: solo lo vimos en `null`. Se guarda como JSON
   crudo y se muestra tal cual en el error.
4. **Zona horaria de `FECHA_DE_EMISION`**: viene sin offset (`...T00:00:00`). Se
   trata como string opaco; no se convierte a tiempo hasta saber que representa.
5. **El schema de una custom query puede cambiar sin avisar.** Por eso el JSONL
   guarda el JSON original y no lo que el struct entiende: si manana la
   consulta 9 suma columnas, aparecen solas en el volcado sin recompilar nada.
6. **Semantica de `TOTAL`**: no esta confirmado si incluye impuestos ni si las NC
   vienen en negativo. Por eso la suma esta apagada por defecto.
7. **Columnas de la consulta de clientes**: no modelamos ninguna. Las filas se
   guardan como el JSON original (`model.RawRow`), porque cerrar un struct a
   ciegas pierde datos en silencio. Se tipean cuando veamos un JSONL real.
8. **Contrato MYLOS**: no existe todavia. No hay ni un stub.
