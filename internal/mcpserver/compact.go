package mcpserver

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/correlation"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/evidence"
)

// wireOverhead accounts for the MCP result envelope around the two payloads.
const wireOverhead = 512

// compactIncident reserves one item per contributing source, fills the item
// limit by newest evidence, then enforces the byte budget and rebuilds the
// summary from the retained items.
func compactIncident(bundle evidence.Bundle, maxItems, byteBudget int) (evidence.Bundle, error) {
	bundle.Compact = true
	bundle.ByteBudget = byteBudget
	items := append([]evidence.Item(nil), bundle.Items...)
	evidence.SortItems(items)

	bySource := make(map[evidence.SourceKind][]evidence.Item)
	for _, item := range items {
		bySource[item.Source] = append(bySource[item.Source], item)
	}
	kinds := make([]string, 0, len(bySource))
	for kind := range bySource {
		kinds = append(kinds, string(kind))
	}
	sort.Strings(kinds)

	selected := make(map[string]evidence.Item, len(items))
	// One newest item for each contributing source.
	for _, kind := range kinds {
		group := bySource[evidence.SourceKind(kind)]
		selected[group[len(group)-1].ID] = group[len(group)-1]
	}
	// Fill the item limit by newest evidence.
	remaining := items
	for index := len(remaining) - 1; index >= 0 && len(selected) < maxItems; index-- {
		if _, ok := selected[remaining[index].ID]; ok {
			continue
		}
		selected[remaining[index].ID] = remaining[index]
	}
	if len(selected) > maxItems {
		selected = trimToNewest(selected, maxItems)
	}
	planned := len(selected)
	selectionOmitted := len(items) - planned

	bundle.Items = selectedItems(selected)
	bundle.SelectionTruncated = selectionOmitted > 0
	bundle.OmittedItems = selectionOmitted
	bundle.Truncated = bundle.Truncated || bundle.SelectionTruncated
	if bundle.OmittedSources == nil {
		bundle.OmittedSources = []string{}
	}
	// Enforce the exact serialized envelope. Every drop refreshes the derived
	// summary and warnings, because both are part of the payload that the
	// budget limits.
	RebuildCompactDerived(&bundle, maxItems)
	if err := finalizeIncidentResponse(&bundle, maxItems); err != nil {
		return bundle, err
	}
	return bundle, nil
}

// finalizeIncidentResponse sets the delivery flag, drops retained items until
// the finalized envelope fits the budget, and fails closed when the source
// metadata alone cannot fit. Every field that changes with an omission is
// measured again before the next decision.
func finalizeIncidentResponse(bundle *evidence.Bundle, maxItems int) error {
	bundle.DeliveryComplete = !bundle.SelectionTruncated && !bundle.ByteBudgetTruncated
	RebuildCompactDerived(bundle, maxItems)
	for bundle.ByteSize > bundle.ByteBudget && len(bundle.Items) > 0 {
		drop, _ := dropCandidate(bundle.Items)
		bundle.Items = append(bundle.Items[:drop], bundle.Items[drop+1:]...)
		bundle.OmittedItems++
		bundle.ByteBudgetTruncated = true
		bundle.Truncated = true
		bundle.DeliveryComplete = false
		RebuildCompactDerived(bundle, maxItems)
	}
	if bundle.ByteSize > bundle.ByteBudget {
		return fmt.Errorf("response budget %d bytes cannot hold the source metadata (%d bytes)", bundle.ByteBudget, bundle.ByteSize)
	}
	return nil
}

// RebuildCompactDerived refreshes the item-derived fields after selection.
func RebuildCompactDerived(bundle *evidence.Bundle, maxItems int) {
	finalizeOmittedSources(bundle)
	correlation.RebuildDerived(bundle, maxItems)
	if bundle.Items == nil {
		bundle.Items = []evidence.Item{}
	}
	if bundle.OmittedSources == nil {
		bundle.OmittedSources = []string{}
	}
	finalizeIncidentByteSize(bundle)
}

func trimToNewest(selected map[string]evidence.Item, max int) map[string]evidence.Item {
	items := selectedItems(selected)
	if len(items) > max {
		items = items[len(items)-max:]
	}
	out := make(map[string]evidence.Item, len(items))
	for _, item := range items {
		out[item.ID] = item
	}
	return out
}

func selectedItems(selected map[string]evidence.Item) []evidence.Item {
	items := make([]evidence.Item, 0, len(selected))
	for _, item := range selected {
		items = append(items, item)
	}
	evidence.SortItems(items)
	return items
}

func dropCandidate(items []evidence.Item) (int, evidence.Item) {
	counts := map[evidence.SourceKind]int{}
	for _, item := range items {
		counts[item.Source]++
	}
	for index, item := range items {
		if counts[item.Source] > 1 {
			return index, item
		}
	}
	return 0, items[0]
}

func finalizeOmittedSources(bundle *evidence.Bundle) {
	selectedSources := map[evidence.SourceKind]bool{}
	for _, item := range bundle.Items {
		selectedSources[item.Source] = true
	}
	omitted := []string{}
	seen := map[string]bool{}
	for _, source := range bundle.Sources {
		if !sourceHadItems(bundle, source.Kind) {
			continue
		}
		if !selectedSources[source.Kind] && !seen[string(source.Kind)] {
			omitted = append(omitted, string(source.Kind))
			seen[string(source.Kind)] = true
		}
	}
	sort.Strings(omitted)
	bundle.OmittedSources = omitted
}

// sourceHadItems reports a source that contributed evidence before selection.
func sourceHadItems(bundle *evidence.Bundle, kind evidence.SourceKind) bool {
	for _, source := range bundle.Sources {
		if source.Kind == kind && source.ItemCount > 0 {
			return true
		}
	}
	return false
}

// incidentWireSize gives the settled size of a bundle copy.
func incidentWireSize(bundle evidence.Bundle) int {
	return finalizeIncidentByteSize(&bundle)
}

// finalizeIncidentByteSize writes the byte_size fixed point into the bundle and
// returns it.
func finalizeIncidentByteSize(bundle *evidence.Bundle) int {
	out := IncidentContextOut(*bundle)
	size := settledWireSize(func() int { return wireSize(out) }, func(size int) { out.ByteSize = size })
	bundle.ByteSize = size
	return size
}

// finalizeServiceStatusSize writes the byte_size fixed point into the response.
func finalizeServiceStatusSize(out *ServiceStatusOut) int {
	return settledWireSize(func() int { return wireSize(*out) }, func(size int) { out.ByteSize = size })
}

// finalizeSearchLogsSize writes the byte_size fixed point into the response.
func finalizeSearchLogsSize(out *SearchLogsOut) int {
	return settledWireSize(func() int { return wireSize(*out) }, func(size int) { out.ByteSize = size })
}

// finalizeFailedCronsSize writes the byte_size fixed point into the response.
func finalizeFailedCronsSize(out *FailedCronsOut) int {
	return settledWireSize(func() int { return wireSize(*out) }, func(size int) { out.ByteSize = size })
}

// finalizeGetEvidenceSize writes the byte_size fixed point into the response.
func finalizeGetEvidenceSize(out *GetEvidenceOut) int {
	return settledWireSize(func() int { return wireSize(*out) }, func(size int) { out.ByteSize = size })
}

// settledWireSize writes candidate sizes into a response's byte_size field
// until the measured payload equals the written value. byte_size describes the
// final response, so it includes the decimal digits of byte_size itself. The
// measured size is monotonic in the field width, so the loop reaches a fixed
// point in a few steps.
func settledWireSize(measure func() int, assign func(int)) int {
	size := measure()
	for {
		assign(size)
		measured := measure()
		if measured == size {
			return size
		}
		size = measured
	}
}

// wireSize measures one serialization of v plus the fixed MCP envelope
// allowance. It uses the current value of the byte_size field in v.
func wireSize(v any) int {
	text, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return 0
	}
	structured, err := json.Marshal(v)
	if err != nil {
		return len(text) + wireOverhead
	}
	return len(text) + len(structured) + wireOverhead
}
