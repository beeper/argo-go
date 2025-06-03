// Package util_test contains tests for the util package.
package util

import (
	"reflect"
	"testing"
)

func TestGroupBy(t *testing.T) {
	type testCase struct {
		name     string
		input    []int
		extract  func(int) int
		expected map[int][]int
	}

	tests := []testCase{
		{
			name:  "group by parity",
			input: []int{1, 2, 3, 4, 5, 6},
			extract: func(i int) int {
				return i % 2
			},
			expected: map[int][]int{
				0: {2, 4, 6},
				1: {1, 3, 5},
			},
		},
		{
			name:  "empty input",
			input: []int{},
			extract: func(i int) int {
				return i % 2
			},
			expected: map[int][]int{},
		},
		{
			name:  "group by value itself",
			input: []int{1, 2, 2, 3, 3, 3},
			extract: func(i int) int {
				return i
			},
			expected: map[int][]int{
				1: {1},
				2: {2, 2},
				3: {3, 3, 3},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			actual := GroupBy(tc.input, tc.extract)
			if !reflect.DeepEqual(actual, tc.expected) {
				t.Errorf("expected %v, got %v", tc.expected, actual)
			}
		})
	}
}
