package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// defaultAPIURL matches the AIRWAY_PORT default documented in .env.example.
const defaultAPIURL = "http://127.0.0.1:1905"

// Client is the thin HTTP client every subcommand shares.
type Client struct {
	baseURL string
	http    *http.Client
}

// NewClient builds a client against A1S_API_URL (default: local api port).
func NewClient() *Client {
	base := os.Getenv("A1S_API_URL")
	if base == "" {
		base = defaultAPIURL
	}

	return &Client{
		baseURL: strings.TrimRight(base, "/"),
		http:    &http.Client{Timeout: 10 * time.Second},
	}
}

// APIError carries the error envelope from a non-2xx API response.
type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return e.Message
	}

	return e.Code
}

// fail prints err the way every subcommand reports failures and returns the
// API-error exit code.
func (c *Client) fail(err error) int {
	var apiErr *APIError
	if asAPIError(err, &apiErr) {
		fmt.Fprintf(os.Stderr, "a1s: %s\n", apiErr.Error())
		return 1
	}

	fmt.Fprintf(os.Stderr, "a1s: cannot reach the API at %s (%v); is `a1s api` running?\n", c.baseURL, err)
	return 1
}

func asAPIError(err error, target **APIError) bool {
	e, ok := err.(*APIError)
	if ok {
		*target = e
	}

	return ok
}

// do performs one JSON request; non-2xx responses become *APIError and
// transport failures keep their wrapped url.Error for fail() to render.
func (c *Client) do(method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
	}

	req, err := http.NewRequest(method, c.baseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		apiErr := &APIError{Status: resp.StatusCode, Code: "internal_error"}
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		var envelope struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(payload, &envelope) == nil && envelope.Error.Code != "" {
			apiErr.Code = envelope.Error.Code
			apiErr.Message = envelope.Error.Message
		}

		return apiErr
	}

	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}

	return nil
}
