package service

import "testing"

// R35 review: a lease label is display text in the Panel's Memory lines.
// Bidirectional overrides, other format characters, C1 controls and line
// separators are dropped; tabs and newlines become spaces.
func TestLabelTextDropsFormatAndControlCharacters(t *testing.T) {
	for in, want := range map[string]string{
		"ComfyUI: Wan 2.2":             "ComfyUI: Wan 2.2",
		"Comfy\u202eUI: model":         "ComfyUI: model",
		"A\u2066B\u2069C":              "ABC",
		"A\u0085B\u009bC\u007fD":       "ABCD",
		"A\u2028B\u2029C":              "ABC",
		"one\nline\ttext":              "one line text",
		"\u200bOpenAbstractions\u200d": "OpenAbstractions",
	} {
		if got := labelText(in); got != want {
			t.Errorf("labelText(%q) = %q, want %q", in, got, want)
		}
	}
}
