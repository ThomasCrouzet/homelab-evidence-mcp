package beszel

import "testing"

func FuzzDecodeSystemPage(f *testing.F) {
	for _, seed := range []string{
		`[]`,
		`{"items":[],"page":1,"perPage":200,"totalItems":0,"totalPages":0}`,
		`{"items":[{"name":"a","info":{"cpu":1e309,"mp":-1}}]}`,
		`{"items":null}`,
		`{"page":-1,"totalPages":999999}`,
		`[[[[[[[[`,
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 64*1024 {
			t.Skip()
		}
		_, _ = decodeSystemPage(raw)
	})
}
