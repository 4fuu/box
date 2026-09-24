package size

import "testing"

func TestParseBytes(t *testing.T) {
	cases := map[string]int64{
		"2G":   2 * G,
		"2g":   2 * G,
		"2GB":  2 * G,
		"2GiB": 2 * G,
		"512M": 512 * M,
		"20G":  20 * G,
		"4096": 4096,
	}
	for in, want := range cases {
		got, err := ParseBytes(in)
		if err != nil || got != want {
			t.Fatalf("%s: %d %v", in, got, err)
		}
		if in == "2G" && FormatBytes(got) != "2G" {
			t.Fatalf("format %s", FormatBytes(got))
		}
	}
	for _, bad := range []string{"", "0", "-1", "G", "2X", "nope"} {
		if _, err := ParseBytes(bad); err == nil {
			t.Fatalf("%s should fail", bad)
		}
	}
}

func TestCPU(t *testing.T) {
	f, err := ParseCPU("2")
	if err != nil || f != 2 || FormatCPU(f) != "2" {
		t.Fatalf("%v %s %v", f, FormatCPU(f), err)
	}
	f, err = ParseCPU("1.5")
	if err != nil || FormatCPU(f) != "1.5" {
		t.Fatalf("%v %v", f, err)
	}
	if _, err := ParseCPU("0"); err == nil {
		t.Fatal("zero")
	}
}
