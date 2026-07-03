package audiotree

import (
	"reflect"
	"testing"

	"app/pkg/ffmpeg"
)

func stacksOf(stacks ...[]int) [][]ffmpeg.FilterType {
	out := make([][]ffmpeg.FilterType, len(stacks))
	for i, stack := range stacks {
		converted := make([]ffmpeg.FilterType, len(stack))
		for j, num := range stack {
			converted[j] = ffmpeg.FilterType(num)
		}
		out[i] = converted
	}
	return out
}

func leaf(i int) *node { return &node{leaf: i} }

func internal(filters []int, children ...*node) *node {
	var converted []ffmpeg.FilterType
	for _, num := range filters {
		converted = append(converted, ffmpeg.FilterType(num))
	}
	return &node{leaf: -1, filters: converted, children: children}
}

func TestBuildTree(t *testing.T) {
	tests := []struct {
		name   string
		stacks [][]ffmpeg.FilterType
		want   *node
	}{
		{
			name:   "no filters",
			stacks: stacksOf([]int{}, []int{}, []int{}),
			want:   internal(nil, leaf(0), leaf(1), leaf(2)),
		},
		{
			name:   "one filter spanning all segments",
			stacks: stacksOf([]int{7}, []int{7}),
			want:   internal([]int{7}, leaf(0), leaf(1)),
		},
		{
			name:   "nested push and pop",
			stacks: stacksOf([]int{}, []int{20}, []int{20, 7}, []int{20}, []int{}),
			want: internal(nil,
				leaf(0),
				internal([]int{20},
					leaf(1),
					internal([]int{7}, leaf(2)),
					leaf(3),
				),
				leaf(4),
			),
		},
		{
			name:   "same-span stack collapses to one node in push order",
			stacks: stacksOf([]int{11, 15}, []int{11, 15}),
			want:   internal([]int{11, 15}, leaf(0), leaf(1)),
		},
		{
			name:   "pop and repush splits spans",
			stacks: stacksOf([]int{6}, []int{}, []int{6}),
			want: internal(nil,
				internal([]int{6}, leaf(0)),
				leaf(1),
				internal([]int{6}, leaf(2)),
			),
		},
		{
			name:   "same filter twice in stack",
			stacks: stacksOf([]int{8, 8}, []int{8}),
			want: internal([]int{8},
				internal([]int{8}, leaf(0)),
				leaf(1),
			),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildTree(tt.stacks)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("buildTree() = %s, want %s", dump(got), dump(tt.want))
			}
		})
	}
}

func dump(n *node) string {
	if n.leaf >= 0 {
		return string(rune('0' + n.leaf))
	}
	s := "{"
	for i, f := range n.filters {
		if i > 0 {
			s += ","
		}
		s += f.Name()
	}
	s += ":"
	for _, c := range n.children {
		s += " " + dump(c)
	}
	return s + "}"
}

func TestParseStackRejectsInvalid(t *testing.T) {
	if _, err := parseStack([]string{"7", "banana"}); err == nil {
		t.Error("expected error for non-numeric filter")
	}
	if _, err := parseStack([]string{"0"}); err == nil {
		t.Error("expected error for out-of-range filter")
	}
	if _, err := parseStack([]string{"99"}); err == nil {
		t.Error("expected error for out-of-range filter")
	}
}
