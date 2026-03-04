package service

import "testing"

var openAISSEUsageBenchSink OpenAIUsage

func BenchmarkParseSSEUsageBytes(b *testing.B) {
	svc := &OpenAIGatewayService{}
	cases := []struct {
		name string
		data []byte
	}{
		{
			name: "terminal_canonical",
			data: []byte(`{"type":"response.completed","response":{"usage":{"input_tokens":11,"output_tokens":7,"input_tokens_details":{"cached_tokens":3}}}}`),
		},
		{
			name: "terminal_spaced",
			data: []byte(`{"type" : "response.done","response":{"usage":{"input_tokens":11,"output_tokens":7,"input_tokens_details":{"cached_tokens":3}}}}`),
		},
		{
			name: "non_terminal",
			data: []byte(`{"type":"response.in_progress","response":{"id":"resp_1"},"padding":"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"}`),
		},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			var usage OpenAIUsage
			for i := 0; i < b.N; i++ {
				svc.parseSSEUsageBytes(tc.data, &usage)
			}
			openAISSEUsageBenchSink = usage
		})
	}
}

func BenchmarkParseSSEUsageString(b *testing.B) {
	svc := &OpenAIGatewayService{}
	cases := []struct {
		name string
		data string
	}{
		{
			name: "terminal_canonical",
			data: `{"type":"response.completed","response":{"usage":{"input_tokens":11,"output_tokens":7,"input_tokens_details":{"cached_tokens":3}}}}`,
		},
		{
			name: "terminal_spaced",
			data: `{"type" : "response.failed","response":{"usage":{"input_tokens":11,"output_tokens":7,"input_tokens_details":{"cached_tokens":3}}}}`,
		},
		{
			name: "non_terminal",
			data: `{"type":"response.in_progress","response":{"id":"resp_1"},"padding":"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"}`,
		},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			var usage OpenAIUsage
			for i := 0; i < b.N; i++ {
				svc.parseSSEUsageString(tc.data, &usage)
			}
			openAISSEUsageBenchSink = usage
		})
	}
}
