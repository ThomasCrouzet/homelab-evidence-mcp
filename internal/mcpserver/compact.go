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
	// Account for item bytes once, then rebuild the envelope from the retained
	// set. The exact check below is the final authority.
	trimmed, err := trimToBudget(&bundle, byteBudget)
	if err != nil {
		return bundle, err
	}
	bundle.ByteBudgetTruncated = trimmed
	bundle.Truncated = bundle.Truncated || trimmed
	RebuildCompactDerived(&bundle, maxItems)
	// The rebuilt summary and warnings are part of the payload. Trim further
	// until the exact serialized envelope fits.
	for bundle.ByteSize > byteBudget && len(bundle.Items) > 0 {
		drop, _ := dropCandidate(bundle.Items)
		bundle.Items = append(bundle.Items[:drop], bundle.Items[drop+1:]...)
		bundle.OmittedItems++
		bundle.ByteBudgetTruncated = true
		bundle.Truncated = true
		RebuildCompactDerived(&bundle, maxItems)
	}
	if bundle.ByteSize > byteBudget {
		return bundle, fmt.Errorf("response budget %d bytes cannot hold the source metadata (%d bytes)", byteBudget, bundle.ByteSize)
	}
	return bundle, nil
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
	bundle.ByteSize = incidentWireSize(*bundle)
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

// trimToBudget removes selected items until the estimated result fits. It
// protects the last selected item of each source until no other choice
// remains. The caller rechecks the exact wire size after the summary rebuild.
func trimToBudget(bundle *evidence.Bundle, budget int) (bool, error) {
	if incidentWireSize(*bundle) <= budget {
		return false, nil
	}
	costs := make(map[string]int, len(bundle.Items))
	for _, item := range bundle.Items {
		costs[item.ID] = itemWireSize(item)
	}
	estimated := incidentWireSize(*bundle)
	trimmed := false
	for len(bundle.Items) > 0 && estimated > budget {
		drop, dropped := dropCandidate(bundle.Items)
		bundle.Items = append(bundle.Items[:drop], bundle.Items[drop+1:]...)
		bundle.OmittedItems++
		estimated -= costs[dropped.ID]
		trimmed = true
	}
	// The envelope and warning text change with each omission. Make the final
	// decision on the exact serialized size.
	for len(bundle.Items) > 0 && incidentWireSize(*bundle) > budget {
		drop, _ := dropCandidate(bundle.Items)
		bundle.Items = append(bundle.Items[:drop], bundle.Items[drop+1:]...)
		bundle.OmittedItems++
		trimmed = true
	}
	bundle.ByteSize = incidentWireSize(*bundle)
	return trimmed, nil
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

func itemWireSize(item evidence.Item) int {
	text, err := json.MarshalIndent(item, "", "  ")
	if err != nil {
		return 0
	}
	structured, err := json.Marshal(item)
	if err != nil {
		return len(text)
	}
	return len(text) + len(structured) + 2
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

func incidentWireSize(bundle evidence.Bundle) int {
	out := IncidentContextOut(bundle)
	text, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return 0
	}
	structured, err := json.Marshal(out)
	if err != nil {
		return len(text) + wireOverhead
	}
	return len(text) + len(structured) + wireOverhead
}

// wireSize estimates the MCP text and structured payload size.
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
