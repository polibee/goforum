package view

import (
	"reflect"
	"testing"
)

func TestPageWindow(t *testing.T) {
	cases := []struct {
		current, total int
		want           []int
	}{
		{1, 1, nil},
		{1, 2, []int{1, 2}},
		{4, 10, []int{1, 0, 2, 3, 4, 5, 6, 0, 10}},
		{1, 10, []int{1, 2, 3, 0, 10}},
		{10, 10, []int{1, 0, 8, 9, 10}},
		{99, 10, []int{1, 0, 8, 9, 10}},
	}
	for _, c := range cases {
		if got := pageWindow(c.current, c.total); !reflect.DeepEqual(got, c.want) {
			t.Fatalf("pageWindow(%d,%d) = %v, want %v", c.current, c.total, got, c.want)
		}
	}
}
