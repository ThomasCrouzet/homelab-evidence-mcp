package loki

import (
	"encoding/json"
	"testing"
)

func FuzzParseNanoAndResponse(f *testing.F) {
	for _, seed := range []string{
		`0`, `-1`, `-1000000000`, `9223372036854775807`, `not-a-number`,
		`{"status":"success","data":{"resultType":"streams","result":[]}}`,
		`{"status":"success","data":{"result":null}}`,
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 64*1024 {
			t.Skip()
		}
		_, _ = parseNano(string(raw))
		var response lokiResponse
		_ = json.Unmarshal(raw, &response)
	})
}
