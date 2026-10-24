package parser

import (
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestJSONContainerReaderFlattensWholeJSONArray(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "compact array",
			input: `[{"a":1},{"b":"x"}]`,
			want:  "{\"a\":1}\n{\"b\":\"x\"}\n",
		},
		{
			name:  "array with whitespace and trailing newline",
			input: "[\n  {\"a\": 1},\n  {\"b\": \"x\"}\n]\n",
			want:  "{\"a\": 1}\n{\"b\": \"x\"}\n",
		},
		{
			name:  "empty array",
			input: "[]",
			want:  "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := io.ReadAll(newJSONContainerJSONLReader(
				strings.NewReader(tt.input),
			))
			require.NoError(t, err)
			assert.Equal(t, tt.want, string(out))
		})
	}
}

func TestJSONContainerReaderPassesThroughNonArrayInput(t *testing.T) {
	input := "{\"a\":1}\n{\"b\":2}\n"
	out, err := io.ReadAll(newJSONContainerJSONLReader(
		strings.NewReader(input),
	))
	require.NoError(t, err)
	assert.Equal(t, input, string(out),
		"JSONL content must pass through unchanged so .json files holding lines keep parsing")
}
