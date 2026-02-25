package service

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"

	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
)

func TestIsOpenAIWSClientDisconnectError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "io_eof", err: io.EOF, want: true},
		{name: "net_closed", err: net.ErrClosed, want: true},
		{name: "context_canceled", err: context.Canceled, want: true},
		{name: "ws_normal_closure", err: coderws.CloseError{Code: coderws.StatusNormalClosure}, want: true},
		{name: "ws_going_away", err: coderws.CloseError{Code: coderws.StatusGoingAway}, want: true},
		{name: "ws_no_status", err: coderws.CloseError{Code: coderws.StatusNoStatusRcvd}, want: true},
		{name: "ws_abnormal_1006", err: coderws.CloseError{Code: coderws.StatusAbnormalClosure}, want: true},
		{name: "ws_policy_violation", err: coderws.CloseError{Code: coderws.StatusPolicyViolation}, want: false},
		{name: "wrapped_eof_message", err: errors.New("failed to get reader: failed to read frame header: EOF"), want: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, isOpenAIWSClientDisconnectError(tt.err))
		})
	}
}
