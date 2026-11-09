package gatus

import (
	"encoding/json"
	"testing"
)

func FuzzDecodeEndpointStatuses(f *testing.F) {
	for _, seed := range []string{
		`[]`,
		`[null]`,
		`[{"name":"a","key":"a","results":[{"success":null,"status":1e309}]}]`,
		`[{"name":"a","key":"a","results":null}]`,
		`[{"key":"a"},{"key":"a"}]`,
		`[[[[[[[[`,
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 64*1024 {
			t.Skip()
		}
		var list []endpointStatus
		if err := json.Unmarshal(raw, &list); err != nil {
			return
		}
		_, _, _ = findEndpoint(list, "a")
	})
}
