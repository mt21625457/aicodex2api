package service

import (
	"errors"
	"fmt"
	"net/http"
	"time"
)

var (
	errOpenAIWSConnClosed               = errors.New("openai ws connection closed")
	errOpenAIWSConnQueueFull            = errors.New("openai ws connection queue full")
	errOpenAIWSPreferredConnUnavailable = errors.New("openai ws preferred connection unavailable")
)

const (
	openAIWSConnHealthCheckTO = 2 * time.Second
)

type openAIWSDialError struct {
	StatusCode      int
	ResponseHeaders http.Header
	Err             error
}

func (e *openAIWSDialError) Error() string {
	if e == nil {
		return ""
	}
	if e.StatusCode > 0 {
		return fmt.Sprintf("openai ws dial failed: status=%d err=%v", e.StatusCode, e.Err)
	}
	return fmt.Sprintf("openai ws dial failed: %v", e.Err)
}

func (e *openAIWSDialError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func cloneHeader(h http.Header) http.Header {
	if h == nil {
		return nil
	}
	cloned := make(http.Header, len(h))
	for k, values := range h {
		copied := make([]string, 0, len(values))
		copied = append(copied, values...)
		cloned[k] = copied
	}
	return cloned
}
