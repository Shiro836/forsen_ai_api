package audiotree

import (
	"fmt"
	"strconv"

	"app/pkg/ffmpeg"
)

// node is either a leaf (one segment, leaf >= 0) or an internal node whose
// children are concatenated and run through filters, innermost first.
type node struct {
	leaf     int
	filters  []ffmpeg.FilterType
	children []*node
}

// buildTree folds per-segment filter-stack snapshots back into the span tree
// the push/pop parsing implies. A filter popped and immediately re-pushed
// between two segments is indistinguishable from one that stayed active, so
// such spans merge; the audible difference is nil for everything but where a
// background loop restarts.
func buildTree(stacks [][]ffmpeg.FilterType) *node {
	root := &node{leaf: -1, children: buildLevel(stacks, 0, len(stacks), 0)}
	collapse(root)
	return root
}

func buildLevel(stacks [][]ffmpeg.FilterType, start, end, depth int) []*node {
	var nodes []*node

	i := start
	for i < end {
		if len(stacks[i]) == depth {
			nodes = append(nodes, &node{leaf: i})
			i++
			continue
		}

		filter := stacks[i][depth]
		j := i + 1
		for j < end && len(stacks[j]) > depth && stacks[j][depth] == filter {
			j++
		}

		nodes = append(nodes, &node{
			leaf:     -1,
			filters:  []ffmpeg.FilterType{filter},
			children: buildLevel(stacks, i, j, depth+1),
		})
		i = j
	}

	return nodes
}

// collapse merges chains of nodes covering identical spans into one node with
// a combined filter list, so a stack of filters over the same segments costs
// a single ffmpeg invocation. Merged filters keep push order — that's the
// order the legacy pipeline applied stacked filters in, and it's what makes
// {2}{4} pitch the voice without pitching backgrounds added later in the
// stack.
func collapse(n *node) {
	for len(n.children) == 1 && n.children[0].leaf < 0 {
		child := n.children[0]
		merged := make([]ffmpeg.FilterType, 0, len(child.filters)+len(n.filters))
		merged = append(merged, n.filters...)
		merged = append(merged, child.filters...)
		n.filters = merged
		n.children = child.children
	}

	for _, child := range n.children {
		if child.leaf < 0 {
			collapse(child)
		}
	}
}

func parseStack(filters []string) ([]ffmpeg.FilterType, error) {
	stack := make([]ffmpeg.FilterType, 0, len(filters))
	for _, name := range filters {
		num, err := strconv.Atoi(name)
		if err != nil {
			return nil, fmt.Errorf("invalid filter number: %q", name)
		}

		filter := ffmpeg.FilterType(num)
		if filter < 1 || filter >= ffmpeg.FilterLast {
			return nil, fmt.Errorf("filter number out of range: %d", num)
		}

		stack = append(stack, filter)
	}

	return stack, nil
}
