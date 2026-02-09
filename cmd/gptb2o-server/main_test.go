package main

import "testing"

func TestAddrForLocalClient(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   string
		want string
	}{
		{in: ":12345", want: "127.0.0.1:12345"},
		{in: "0.0.0.0:12345", want: "127.0.0.1:12345"},
		{in: "[::]:12345", want: "127.0.0.1:12345"},
		{in: "127.0.0.1:12345", want: "127.0.0.1:12345"},
		{in: "[::1]:12345", want: "[::1]:12345"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			if got := addrForLocalClient(tc.in); got != tc.want {
				t.Fatalf("addrForLocalClient(%q)=%q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
