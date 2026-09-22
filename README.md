# mylos-tango-agent

Agente on-premise que lee Tango Punto de Venta (LAN) y, mas adelante, empuja los
datos a MYLOS. Un unico `.exe` de Windows, sin dependencias externas ni runtime.

```
Tango local :17000
    | HTTP LAN
mylos-tango-agent.exe
    | HTTPS outbound
MYLOS API        <- TODAVIA NO IMPLEMENTADO (fase 2)
```

MYLOS cloud nunca se conecta a Tango: el agente es el unico que entra a la LAN, y
siempre sale hacia afuera.

## Estado: FASE 1 - solo lectura de Tango

Hoy el agente sabe hacer una sola cosa: leer la consulta Tango Live
"Detalle de comprobantes" (process 17839) por rango de fechas, recorrer todas las
paginas, validar la respuesta e imprimir un resumen. Opcionalmente vuelca las
filas a un JSONL para inspeccion manual.

No hay (a proposito): Windows Service, base local, scheduler, cliente MYLOS,
colas ni observabilidad. Se agregan cuando haya contrato real.

## Configuracion

Todo por variables de entorno. Nunca hay secretos en el codigo ni en el repo.

| Variable | Obligatoria | Default | Que es |
|---|---|---|---|
| `TANGO_BASE_URL` | si | - | `http://arenales_tango:17000` |
| `TANGO_API_TOKEN` | si | - | token de Apertura API. **Secreto.** |
| `TANGO_COMPANY_ID` | si | - | header `Company` (en Perfumum: `2`) |
| `TANGO_SALES_PROCESS_ID` | si | - | process de la consulta Live (`17839`) |
| `TANGO_HTTP_TIMEOUT` | no | `60s` | timeout por request |
| `TANGO_MAX_RETRIES` | no | `3` | reintentos ante errores transitorios |
| `TANGO_RETRY_BASE_DELAY` | no | `500ms` | base del backoff exponencial con jitter |
| `TANGO_DATE_FORMAT` | no | `02/01/2006` | layout Go de `fromDate`/`toDate`. `dd/MM/yyyy` es el formato observado en Tango |

Se pueden cargar desde un archivo (`--env-file`, por defecto `.env` del directorio
actual). El entorno real siempre gana sobre el archivo.

```bash
cp .env.example .env    # completar el token; .env esta en .gitignore
```

## Uso

```bash
mylos-tango-agent sync-sales --from 01/09/2026 --to 09/09/2026
```

Opciones de `sync-sales`:

| Flag | Default | Que hace |
|---|---|---|
| `--from`, `--to` | - | obligatorias. `DD/MM/AAAA` o `AAAA-MM-DD` |
| `--page-size` | `500` | filas por pagina |
| `--max-pages` | `0` | cortar tras N paginas (0 = todas). Util para probar |
| `--out` | - | **opt-in.** JSONL con una fila por renglon, tal cual la mando Tango. Ver *Datos sensibles* |
| `--sum-amounts` | `false` | acumular `TOTAL` y `CANTIDAD` en el resumen (numero orientativo) |
| `--custom-query` | - | parametro `customQuery` de la consulta Live |
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
== Resumen sync-sales ==
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
cmd/agent/          CLI: flags, subcomandos, impresion del resumen
internal/config/    carga y validacion de configuracion (env / archivo)
internal/tango/     cliente HTTP de Tango: endpoint, retry, errores tipados
internal/sync/      recorrido de paginas, resumen, volcado JSONL
internal/model/     structs calcados de la respuesta real de Tango
```

## Seguridad

- El token viaja solo en el header `ApiAuthorization`, nunca en la URL.
- Nunca se loguea: `config.Config` implementa `slog.LogValuer` y lo enmascara.
- Si el server devuelve el token dentro de un body de error, el cliente lo
  reemplaza por `[redacted]` antes de armar el error (hay test que lo verifica).
- `.env` esta en `.gitignore`; `.env.example` no tiene secretos.

### Datos sensibles

`--out` es **opt-in** y nunca se activa solo. El JSONL que genera contiene datos
reales de negocio: razon social del cliente, nombre del vendedor, articulos,
cantidades e importes. Es PII y es informacion comercial.

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
2. **Formato de `fromDate`/`toDate`**: el default `02/01/2006` (`dd/MM/yyyy`) es
   lo observado en Tango, pero no esta verificado contra el server real todavia.
   `TANGO_DATE_FORMAT` permite cambiarlo sin recompilar.
3. **Forma de `exceptionInfo`**: solo lo vimos en `null`. Se guarda como JSON
   crudo y se muestra tal cual en el error.
4. **Zona horaria de `FECHA_DE_EMISION`**: viene sin offset (`...T00:00:00`). Se
   trata como string opaco; no se convierte a tiempo hasta saber que representa.
5. **Semantica de `TOTAL`**: no esta confirmado si incluye impuestos ni si las NC
   vienen en negativo. Por eso la suma esta apagada por defecto.
6. **Contrato MYLOS**: no existe todavia. No hay ni un stub.
