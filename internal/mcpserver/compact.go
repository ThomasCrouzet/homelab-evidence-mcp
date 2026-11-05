package mcpserver

import (
	"encoding/json"
	"sort"

	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/evidence"
)

// wireOverhead accounts for the MCP result envelope around the two payloads.
const wireOverhead = 512

// compactIncident reserves one item per contributing source, fills the item
// limit by newest evidence, then enforces the byte budget by dropping the
// oldest item from a source that still has another item.
func compactIncident(bundle evidence.Bundle, maxItems, byteBudget int) evidence.Bundle {
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
	trimmed := trimToBudget(&bundle, byteBudget)
	bundle.Truncated = bundle.Truncated || bundle.SelectionTruncated || trimmed
	if trimmed {
		bundle.ByteBudgetTruncated = true
	}
	bundle.ByteSize = incidentWireSize(bundle)
	return bundle
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

// trimToBudget removes selected items until the result fits. It protects the
// last selected item of each source until no other choice remains.
func trimToBudget(bundle *evidence.Bundle, budget int) bool {
	trimmed := false
	for len(bundle.Items) > 0 && incidentWireSize(*bundle) > budget {
		counts := map[evidence.SourceKind]int{}
		for _, item := range bundle.Items {
			counts[item.Source]++
		}
		drop := -1
		for index, item := range bundle.Items {
			if counts[item.Source] > 1 {
				drop = index
				break
			}
		}
		if drop < 0 {
			drop = 0
		}
		bundle.Items = append(bundle.Items[:drop], bundle.Items[drop+1:]...)
		bundle.OmittedItems++
		trimmed = true
	}
	selectedSources := map[evidence.SourceKind]bool{}
	for _, item := range bundle.Items {
		selectedSources[item.Source] = true
	}
	var omitted []string
	seen := map[string]bool{}
	for _, source := range bundle.Sources {
		if _, ok := sourceItemsByKind(bundle, source.Kind); !ok {
			continue
		}
		if !selectedSources[source.Kind] && !seen[string(source.Kind)] {
			omitted = append(omitted, string(source.Kind))
			seen[string(source.Kind)] = true
		}
	}
	sort.Strings(omitted)
	bundle.OmittedSources = omitted
	return trimmed
}

// sourceItemsByKind is a small guard so omitted sources are only reported for
// sources that originally contributed items.
func sourceItemsByKind(bundle *evidence.Bundle, kind evidence.SourceKind) (struct{}, bool) {
	for _, item := range bundle.Items {
		if item.Source == kind {
			return struct{}{}, true
		}
	}
	// The items are already trimmed. Use the source outcome count, which the
	// adapter set before selection.
	for _, source := range bundle.Sources {
		if source.Kind == kind && source.ItemCount > 0 {
			return struct{}{}, true
		}
	}
	return struct{}{}, false
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
