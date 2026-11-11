package dialect

import "testing"

func TestEnsureMySQLParams(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "adds parseTime when missing",
			in:   "root@tcp(127.0.0.1:3306)/forum",
			want: "root@tcp(127.0.0.1:3306)/forum?charset=utf8mb4&loc=UTC&parseTime=true",
		},
		{
			name: "keeps existing parseTime",
			in:   "u:p@tcp(h:3306)/db?charset=utf8mb4&parseTime=True&loc=UTC",
			want: "u:p@tcp(h:3306)/db?charset=utf8mb4&loc=UTC&parseTime=True",
		},
		{
			name: "preserves unrelated params",
			in:   "u@tcp(h:3306)/db?timeout=5s",
			want: "u@tcp(h:3306)/db?charset=utf8mb4&loc=UTC&parseTime=true&timeout=5s",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ensureMySQLParams(tc.in)
			if got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}
