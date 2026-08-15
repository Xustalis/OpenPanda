package util

import "testing"

func TestTokenizeWords(t *testing.T) {
	got := Tokenize("Go core, 部署 TypeScript")
	for _, want := range []string{"go", "core", "typescript", "部", "署"} {
		if _, ok := got[want]; !ok {
			t.Errorf("Tokenize missing %q in %v", want, got)
		}
	}
	if len(got) != 5 {
		t.Errorf("Tokenize = %v, want exactly 5 tokens", got)
	}
}

func TestTokenizeLowercasesAndDropsPunctuation(t *testing.T) {
	got := Tokenize("TypeScript!!!")
	if len(got) != 1 {
		t.Fatalf("Tokenize = %v, want one token", got)
	}
	if _, ok := got["typescript"]; !ok {
		t.Fatalf("Tokenize = %v, want lowercase typescript", got)
	}
}

func TestIsCJKFunctionWord(t *testing.T) {
	cases := []struct {
		w    string
		want bool
	}{
		{"的", true},
		{"是", true},
		{"到", true},
		{"题", false},  // content ideograph
		{"主题", false}, // multi-char: never a function word
		{"", false},
		{"go", false},
	}
	for _, tc := range cases {
		if got := IsCJKFunctionWord(tc.w); got != tc.want {
			t.Errorf("IsCJKFunctionWord(%q) = %v, want %v", tc.w, got, tc.want)
		}
	}
}
