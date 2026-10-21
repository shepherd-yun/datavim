package es

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Sentinels the APIError of a status maps to.
var (
	ErrAuthFailed    = errors.New("authentication failed")
	ErrForbidden     = errors.New("permission denied")
	ErrNotFound      = errors.New("not found")
	ErrConflict      = errors.New("version conflict")
	ErrUnsupported   = errors.New("not supported by this cluster")
	ErrTooLarge      = errors.New("response larger than 50 MB; add size / filter_path")
	ErrCursorExpired = errors.New("the result cursor expired")
)

// APIError is an error response.
type APIError struct {
	Status       int
	Type, Reason string
	RootTypes    []string // root_cause types
	Raw          []byte
}

func (e *APIError) Error() string {
	switch {
	case e.Type != "" && e.Reason != "":
		return e.Type + ": " + e.Reason
	case e.Reason != "":
		return e.Reason
	case e.Type != "":
		return e.Type
	}
	return fmt.Sprintf("HTTP %d", e.Status)
}

func (e *APIError) hasType(t string) bool {
	if e.Type == t {
		return true
	}
	for _, r := range e.RootTypes {
		if r == t {
			return true
		}
	}
	return false
}

func (e *APIError) Is(target error) bool {
	switch target {
	case ErrAuthFailed:
		return e.Status == 401
	case ErrForbidden:
		return e.Status == 403
	case ErrNotFound:
		return e.Status == 404
	case ErrConflict:
		return e.Status == 409
	case ErrUnsupported:
		return e.Status == 405 || e.Status == 400 && strings.Contains(e.Reason, "no handler")
	case ErrCursorExpired:
		return (e.Status == 404 || e.Status == 400 || e.Status == 500) && e.hasType("search_context_missing_exception")
	}
	return false
}

// CheckStatus is nil for a 2xx response and an *APIError otherwise.
func CheckStatus(resp Response) error {
	if resp.Status >= 200 && resp.Status < 300 {
		return nil
	}
	e := &APIError{Status: resp.Status, Raw: resp.Body}
	var body struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(resp.Body, &body) == nil && len(body.Error) > 0 {
		var obj struct {
			Type      string `json:"type"`
			Reason    string `json:"reason"`
			RootCause []struct {
				Type string `json:"type"`
			} `json:"root_cause"`
		}
		var s string
		switch {
		case json.Unmarshal(body.Error, &s) == nil:
			e.Reason = s
		case json.Unmarshal(body.Error, &obj) == nil:
			e.Type, e.Reason = obj.Type, obj.Reason
			for _, r := range obj.RootCause {
				e.RootTypes = append(e.RootTypes, r.Type)
			}
		}
	}
	return e
}

// do sends r and checks its status.
func do(ctx context.Context, d Doer, r Request) (Response, error) {
	resp, err := d.Do(ctx, r)
	if err != nil {
		return resp, err
	}
	return resp, CheckStatus(resp)
}
