package healthchecks

import "testing"

func FuzzDecodeFlips(f *testing.F) {
	for _, seed := range []string{
		`[]`,
		`{"flips":[]}`,
		`{"flips":null}`,
		`[{"timestamp":"invalid","up":7}]`,
		`[{"timestamp":"2026-07-25T10:00:00Z","up":0}]`,
		`[[[[[[[[`,
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 64*1024 {
			t.Skip()
		}
		_, _ = decodeFlips(raw)
	})
}
