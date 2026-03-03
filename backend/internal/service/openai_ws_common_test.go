package service

import (
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenAIWSDialError_Behavior(t *testing.T) {
	t.Parallel()

	var nilErr *openAIWSDialError
	require.Equal(t, "", nilErr.Error())
	require.Nil(t, nilErr.Unwrap())

	err := &openAIWSDialError{
		StatusCode: 429,
		Err:        errors.New("too many requests"),
	}
	require.Contains(t, err.Error(), "status=429")
	require.ErrorIs(t, err.Unwrap(), err.Err)
}

func TestCloneHeader_DeepCopy(t *testing.T) {
	t.Parallel()

	require.Nil(t, cloneHeader(nil))

	origin := http.Header{
		"X-Request-Id": []string{"req-1"},
		"Set-Cookie":   []string{"a=1", "b=2"},
	}
	cloned := cloneHeader(origin)
	require.Equal(t, origin.Get("X-Request-Id"), cloned.Get("X-Request-Id"))
	require.Equal(t, origin.Values("Set-Cookie"), cloned.Values("Set-Cookie"))

	// 修改拷贝不应污染原 header。
	cloned.Set("X-Request-Id", "req-2")
	cloned["Set-Cookie"][0] = "a=9"
	require.Equal(t, "req-1", origin.Get("X-Request-Id"))
	require.Equal(t, "a=1", origin.Values("Set-Cookie")[0])
}
