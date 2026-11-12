package realtime

import "testing"

func TestShowIDFromChannel(t *testing.T) {
	tests := []struct {
		channel string
		want    string
	}{
		{channel: "show:show-1:events", want: "show-1"},
		{channel: "show:abc:def:events", want: "abc:def"},
		{channel: "product:show-1:events", want: ""},
		{channel: "show:show-1:other", want: ""},
		{channel: "show::events", want: ""},
	}
	for _, test := range tests {
		t.Run(test.channel, func(t *testing.T) {
			if got := showIDFromChannel(test.channel); got != test.want {
				t.Fatalf("showIDFromChannel(%q) = %q, want %q", test.channel, got, test.want)
			}
		})
	}
}
