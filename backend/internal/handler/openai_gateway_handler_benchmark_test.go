package handler

import (
	"fmt"
	"testing"
)

var openAIWSTurnScopedRequestIDSink string

func BenchmarkOpenAIWSTurnScopedFallbackRequestID(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		openAIWSTurnScopedRequestIDSink = openAIWSTurnScopedFallbackRequestID("req_bench_123456", 9)
	}
}

func BenchmarkOpenAIWSTurnScopedFallbackRequestID_FmtSprintf(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		openAIWSTurnScopedRequestIDSink = fmt.Sprintf("%s:turn:%d", "req_bench_123456", 9)
	}
}
