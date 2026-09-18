// temperature.go preserves explicit sampling values across an SDK request
// shape that otherwise drops zero through json omitempty.
package llm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"

	openai "github.com/sashabaranov/go-openai"
)

// maxChatRequestBytes bounds the adapter repair. The SDK has already encoded
// the request in memory; this prevents an unexpected caller from making the
// compatibility layer allocate without limit.
const maxChatRequestBytes = 64 << 20

type temperatureContextKey struct{}

// temperatureHTTPDoer is scoped to one LLM client. It adds the caller's
// explicit temperature to chat-completion JSON after the SDK encodes the
// request. This is necessary for zero: the pinned SDK declares temperature as
// a scalar with `omitempty`, so setting zero on its struct omits the field.
type temperatureHTTPDoer struct {
	next openai.HTTPDoer
}

func (d temperatureHTTPDoer) Do(req *http.Request) (*http.Response, error) {
	temperature, ok := req.Context().Value(temperatureContextKey{}).(float32)
	if !ok || req.Method != http.MethodPost || !strings.HasSuffix(req.URL.Path, "/chat/completions") {
		return d.next.Do(req)
	}
	if req.Body == nil {
		return nil, temperatureRequestError("chat request has no body", nil)
	}

	raw, err := io.ReadAll(io.LimitReader(req.Body, maxChatRequestBytes+1))
	closeErr := req.Body.Close()
	if err != nil {
		return nil, temperatureRequestError("read request", err)
	}
	if closeErr != nil {
		return nil, temperatureRequestError("close request", closeErr)
	}
	if len(raw) > maxChatRequestBytes {
		return nil, temperatureRequestError(fmt.Sprintf("chat request exceeds %d bytes", maxChatRequestBytes), nil)
	}

	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, temperatureRequestError("decode request", err)
	}
	encodedTemperature, err := json.Marshal(temperature)
	if err != nil {
		return nil, temperatureRequestError("encode value", err)
	}
	body["temperature"] = encodedTemperature
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, temperatureRequestError("encode request", err)
	}

	clone := req.Clone(req.Context())
	clone.Body = io.NopCloser(bytes.NewReader(encoded))
	clone.ContentLength = int64(len(encoded))
	clone.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(encoded)), nil
	}
	return d.next.Do(clone)
}

func validateTemperature(temperature *float32) error {
	if temperature == nil {
		return nil
	}
	value := float64(*temperature)
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 2 {
		return fmt.Errorf("%w: llm temperature must be finite and between 0 and 2", ErrInvalidRequest)
	}
	return nil
}

func temperatureRequestError(message string, cause error) error {
	if cause == nil {
		return fmt.Errorf("%w: llm temperature repair: %s", ErrInvalidRequest, message)
	}
	return fmt.Errorf("%w: llm temperature repair: %s: %v", ErrInvalidRequest, message, cause)
}
