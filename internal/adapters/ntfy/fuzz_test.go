package ntfy

import "testing"

func FuzzDecodeMessages(f *testing.F) {
	for _, seed := range []string{
		`[]`,
		`[null]`,
		`[{"id":"a","time":0,"event":"message","topic":"t","message":"m"}]`,
		`[{"id":"a"}`,
		`[]garbage`,
		`{"id":"a","time":1,"event":"message","topic":"t"}` + "\n" + `{not-json}`,
		`"\ud800"`,
		`[[[[[[[[[[[[[[[[[[[[`,
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 64*1024 {
			t.Skip()
		}
		_, _, _ = decodeMessages(raw, false)
		_, _, _ = decodeMessages(raw, true)
	})
}
