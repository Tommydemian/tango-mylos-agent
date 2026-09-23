package mylos

import "fmt"

// HTTPError: MYLOS respondio con un status no-2xx.
// Body viene truncado y con el token enmascarado.
type HTTPError struct {
	StatusCode int
	Status     string
	Body       string
}

func (e *HTTPError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("mylos: respuesta HTTP %s", e.Status)
	}
	return fmt.Sprintf("mylos: respuesta HTTP %s: %s", e.Status, e.Body)
}

// Retryable indica si conviene reintentar este status.
func (e *HTTPError) Retryable() bool { return isRetryableStatus(e.StatusCode) }

// ResponseError: la respuesta no tiene la forma esperada.
type ResponseError struct {
	Reason string
}

func (e *ResponseError) Error() string {
	return "mylos: respuesta inesperada: " + e.Reason
}

func isRetryableStatus(code int) bool {
	switch code {
	case 408, 425, 429:
		return true
	}
	return code >= 500 && code <= 599
}
