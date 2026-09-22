package tango

import "fmt"

// HTTPError: el server respondio con un status no-2xx.
// Body viene truncado. Nunca incluye el token: el token viaja en un header
// de request y jamas se copia a los errores.
type HTTPError struct {
	StatusCode int
	Status     string
	Body       string
}

func (e *HTTPError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("tango: respuesta HTTP %s", e.Status)
	}
	return fmt.Sprintf("tango: respuesta HTTP %s: %s", e.Status, e.Body)
}

// Retryable indica si conviene reintentar este status.
func (e *HTTPError) Retryable() bool { return isRetryableStatus(e.StatusCode) }

// APIError: HTTP 200 pero la API respondio succeeded=false.
type APIError struct {
	Message       string
	ExceptionInfo string
}

func (e *APIError) Error() string {
	switch {
	case e.Message != "" && e.ExceptionInfo != "":
		return fmt.Sprintf("tango: la consulta fallo (succeeded=false): %s (%s)", e.Message, e.ExceptionInfo)
	case e.Message != "":
		return fmt.Sprintf("tango: la consulta fallo (succeeded=false): %s", e.Message)
	case e.ExceptionInfo != "":
		return fmt.Sprintf("tango: la consulta fallo (succeeded=false): %s", e.ExceptionInfo)
	default:
		return "tango: la consulta fallo (succeeded=false) sin detalle"
	}
}

// ResponseError: la respuesta no tiene la forma esperada.
type ResponseError struct {
	Reason string
}

func (e *ResponseError) Error() string {
	return "tango: respuesta inesperada: " + e.Reason
}

func isRetryableStatus(code int) bool {
	switch code {
	case 408, 425, 429:
		return true
	}
	return code >= 500 && code <= 599
}
