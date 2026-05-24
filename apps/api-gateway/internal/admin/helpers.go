package admin

import (
	"encoding/json"
	"io"
	"net/http"
)

// readBodyInto decodes the JSON request body into dst.
func readBodyInto(r *http.Request, dst interface{}) error {
	r.Body = http.MaxBytesReader(nil, r.Body, 64*1024)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, dst)
}
