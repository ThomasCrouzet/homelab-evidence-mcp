package docker

import (
	"encoding/json"
	"testing"
)

func FuzzDecodeContainers(f *testing.F) {
	for _, seed := range []string{
		`[]`,
		`[null]`,
		`[{"Id":"a","Names":["/app"],"State":"running"}]`,
		`[{"Id":"a","Names":["/App","/app"]},{"Id":"b","Names":["/app"]}]`,
		`[{"Names":null}]`,
		`[[[[[[[[`,
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 64*1024 {
			t.Skip()
		}
		var list []containerSummary
		if err := json.Unmarshal(raw, &list); err != nil {
			return
		}
		_, _, _ = findContainer(list, "app")
	})
}
