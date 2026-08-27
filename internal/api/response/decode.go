package response

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
)

const maxBodySize = 16 << 10

// DecodeJSON reads a single JSON object of up to maxBodySize bytes into dst.
// Unknown fields are ignored; a second JSON value after the object is rejected.
func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	if err := requireJSONContentType(r); err != nil {
		return err
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)

	dec := json.NewDecoder(r.Body)

	if err := dec.Decode(dst); err != nil {
		return describeDecodeError(err)
	}
	if dec.More() {
		return errors.New("request body must contain a single JSON object")
	}
	return nil
}

func requireJSONContentType(r *http.Request) error {
	ct := r.Header.Get("Content-Type")
	if ct == "" {
		return errors.New("Content-Type must be application/json")
	}
	mediaType, _, err := mime.ParseMediaType(ct)
	if err != nil || mediaType != "application/json" {
		return errors.New("Content-Type must be application/json")
	}
	return nil
}

func describeDecodeError(err error) error {
	var syntaxErr *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	var maxBytesErr *http.MaxBytesError

	switch {
	case errors.As(err, &syntaxErr):
		return errors.New("request body is not valid JSON")
	case errors.As(err, &typeErr):
		if typeErr.Field != "" {
			return errors.New("field " + typeErr.Field + " has the wrong type")
		}
		return errors.New("request body is not a JSON object")
	case errors.As(err, &maxBytesErr):
		return errors.New("request body is too large")
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return errors.New("request body is empty or truncated")
	default:
		return errors.New("request body could not be read")
	}
}
