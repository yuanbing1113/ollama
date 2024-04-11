package model

import (
	"bytes"
	"cmp"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"
)

type fields struct {
	host, namespace, model, tag, build string
	digest                             string
}

func fieldsFromName(p Name) fields {
	return fields{
		host:      p.parts[PartHost],
		namespace: p.parts[PartNamespace],
		model:     p.parts[PartModel],
		tag:       p.parts[PartTag],
		build:     p.parts[PartBuild],
		digest:    p.parts[PartDigest],
	}
}

var testNames = map[string]fields{
	"mistral:latest":                 {model: "mistral", tag: "latest"},
	"mistral":                        {model: "mistral"},
	"mistral:30B":                    {model: "mistral", tag: "30B"},
	"mistral:7b":                     {model: "mistral", tag: "7b"},
	"mistral:7b+Q4_0":                {model: "mistral", tag: "7b", build: "Q4_0"},
	"mistral+KQED":                   {model: "mistral", build: "KQED"},
	"mistral.x-3:7b+Q4_0":            {model: "mistral.x-3", tag: "7b", build: "Q4_0"},
	"mistral:7b+q4_0":                {model: "mistral", tag: "7b", build: "q4_0"},
	"llama2":                         {model: "llama2"},
	"user/model":                     {namespace: "user", model: "model"},
	"example.com/ns/mistral:7b+Q4_0": {host: "example.com", namespace: "ns", model: "mistral", tag: "7b", build: "Q4_0"},
	"example.com/ns/mistral:7b+X":    {host: "example.com", namespace: "ns", model: "mistral", tag: "7b", build: "X"},

	// invalid digest
	"mistral:latest@invalid256-": {},
	"mistral:latest@-123":        {},
	"mistral:latest@!-123":       {},
	"mistral:latest@1-!":         {},
	"mistral:latest@":            {},

	// resolved
	"x@sha123-1": {model: "x", digest: "sha123-1"},
	"@sha456-2":  {digest: "sha456-2"},

	"@@sha123-1": {},

	// preserves case for build
	"x+b": {model: "x", build: "b"},

	// invalid (includes fuzzing trophies)
	" / / : + ": {},
	" / : + ":   {},
	" : + ":     {},
	" + ":       {},
	" : ":       {},
	" / ":       {},
	" /":        {},
	"/ ":        {},
	"/":         {},
	":":         {},
	"+":         {},

	// (".") in namepsace is not allowed
	"invalid.com/7b+x": {},

	"invalid:7b+Q4_0:latest": {},
	"in valid":               {},
	"invalid/y/z/foo":        {},
	"/0":                     {},
	"0 /0":                   {},
	"0 /":                    {},
	"0/":                     {},
	":/0":                    {},
	"+0/00000":               {},
	"0+.\xf2\x80\xf6\x9d00000\xe5\x99\xe6\xd900\xd90\xa60\x91\xdc0\xff\xbf\x99\xe800\xb9\xdc\xd6\xc300\x970\xfb\xfd0\xe0\x8a\xe1\xad\xd40\x9700\xa80\x980\xdd0000\xb00\x91000\xfe0\x89\x9b\x90\x93\x9f0\xe60\xf7\x84\xb0\x87\xa5\xff0\xa000\x9a\x85\xf6\x85\xfe\xa9\xf9\xe9\xde00\xf4\xe0\x8f\x81\xad\xde00\xd700\xaa\xe000000\xb1\xee0\x91": {},
	"0//0":                        {},
	"m+^^^":                       {},
	"file:///etc/passwd":          {},
	"file:///etc/passwd:latest":   {},
	"file:///etc/passwd:latest+u": {},

	":x": {},
	"+x": {},
	"x+": {},

	// Disallow ("\.+") in any part to prevent path traversal anywhere
	// we convert the name to a path.
	"../etc/passwd":  {},
	".../etc/passwd": {},
	"./../passwd":    {},
	"./0+..":         {},

	strings.Repeat("a", MaxNamePartLen):   {model: strings.Repeat("a", MaxNamePartLen)},
	strings.Repeat("a", MaxNamePartLen+1): {},
}

// TestConsecutiveDots tests that consecutive dots are not allowed in any
// part, to avoid path traversal. There also are some tests in testNames, but
// this test is more exhaustive and exists to emphasize the importance of
// preventing path traversal.
func TestNameConsecutiveDots(t *testing.T) {
	for i := 1; i < 10; i++ {
		s := strings.Repeat(".", i)
		if i > 1 {
			if g := ParseNameFill(s, "").String(); g != "" {
				t.Errorf("ParseName(%q) = %q; want empty string", s, g)
			}
		} else {
			if g := ParseNameFill(s, "").String(); g != s {
				t.Errorf("ParseName(%q) = %q; want %q", s, g, s)
			}
		}
	}
}

func TestNameParts(t *testing.T) {
	var p Name
	if w, g := int(PartDigest+1), len(p.Parts()); w != g {
		t.Errorf("Parts() = %d; want %d", g, w)
	}
}

func TestNamePartString(t *testing.T) {
	if g := PartKind(-2).String(); g != "Unknown" {
		t.Errorf("Unknown part = %q; want %q", g, "Unknown")
	}
	for kind, name := range kindNames {
		if g := kind.String(); g != name {
			t.Errorf("%s = %q; want %q", kind, g, name)
		}
	}
}

func TestParseName(t *testing.T) {
	for baseName, want := range testNames {
		for _, prefix := range []string{"", "https://", "http://"} {
			// We should get the same results with or without the
			// http(s) prefixes
			s := prefix + baseName

			t.Run(s, func(t *testing.T) {
				name := ParseNameFill(s, "")
				got := fieldsFromName(name)
				if got != want {
					t.Errorf("ParseName(%q) = %q; want %q", s, got, want)
				}

				// test round-trip
				if !ParseNameFill(name.String(), "").EqualFold(name) {
					t.Errorf("ParseName(%q).String() = %s; want %s", s, name.String(), baseName)
				}
			})
		}
	}
}

func TestCompleteWithAndWithoutBuild(t *testing.T) {
	cases := []struct {
		in              string
		complete        bool
		completeNoBuild bool
	}{
		{"", false, false},
		{"incomplete/mistral:7b+x", false, false},
		{"incomplete/mistral:7b+Q4_0", false, false},
		{"incomplete:7b+x", false, false},
		{"complete.com/x/mistral:latest+Q4_0", true, true},
		{"complete.com/x/mistral:latest", false, true},
	}

	for _, tt := range cases {
		t.Run(tt.in, func(t *testing.T) {
			p := ParseNameFill(tt.in, "")
			t.Logf("ParseName(%q) = %#v", tt.in, p)
			if g := p.IsComplete(); g != tt.complete {
				t.Errorf("Complete(%q) = %v; want %v", tt.in, g, tt.complete)
			}
			if g := p.IsCompleteNoBuild(); g != tt.completeNoBuild {
				t.Errorf("CompleteNoBuild(%q) = %v; want %v", tt.in, g, tt.completeNoBuild)
			}
		})
	}

	// Complete uses Parts which returns a slice, but it should be
	// inlined when used in Complete, preventing any allocations or
	// escaping to the heap.
	allocs := testing.AllocsPerRun(1000, func() {
		keep(ParseNameFill("complete.com/x/mistral:latest+Q4_0", "").IsComplete())
	})
	if allocs > 0 {
		t.Errorf("Complete allocs = %v; want 0", allocs)
	}
}

func TestNameLogValue(t *testing.T) {
	cases := []string{
		"example.com/library/mistral:latest+Q4_0",
		"mistral:latest",
		"mistral:7b+Q4_0",
	}
	for _, s := range cases {
		t.Run(s, func(t *testing.T) {
			var b bytes.Buffer
			log := slog.New(slog.NewTextHandler(&b, nil))
			name := ParseNameFill(s, "")
			log.Info("", "name", name)
			want := fmt.Sprintf("name=%s", name.GoString())
			got := b.String()
			if !strings.Contains(got, want) {
				t.Errorf("expected log output to contain %q; got %q", want, got)
			}
		})
	}
}

func TestNameGoString(t *testing.T) {
	cases := []struct {
		name         string
		in           string
		wantString   string
		wantGoString string // default is tt.in
	}{
		{
			name:         "Complete Name",
			in:           "example.com/library/mistral:latest+Q4_0",
			wantGoString: "example.com/library/mistral:latest+Q4_0@?",
		},
		{
			name:         "Short Name",
			in:           "mistral:latest",
			wantGoString: "?/?/mistral:latest+?@?",
		},
		{
			name:         "Long Name",
			in:           "library/mistral:latest",
			wantGoString: "?/library/mistral:latest+?@?",
		},
		{
			name:         "Case Preserved",
			in:           "Library/Mistral:Latest",
			wantGoString: "?/Library/Mistral:Latest+?@?",
		},
		{
			name:         "With digest",
			in:           "Library/Mistral:Latest@sha256-123456",
			wantGoString: "?/Library/Mistral:Latest+?@sha256-123456",
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			p := ParseNameFill(tt.in, "")
			tt.wantGoString = cmp.Or(tt.wantGoString, tt.in)
			if g := fmt.Sprintf("%#v", p); g != tt.wantGoString {
				t.Errorf("GoString() = %q; want %q", g, tt.wantGoString)
			}
		})
	}
}

func TestDisplayShortest(t *testing.T) {
	cases := []struct {
		in        string
		mask      string
		want      string
		wantPanic bool
	}{
		{"example.com/library/mistral:latest+Q4_0", "example.com/library/_:latest", "mistral", false},
		{"example.com/library/mistral:latest+Q4_0", "example.com/_/_:latest", "library/mistral", false},
		{"example.com/library/mistral:latest+Q4_0", "", "example.com/library/mistral", false},
		{"example.com/library/mistral:latest+Q4_0", "", "example.com/library/mistral", false},

		// case-insensitive
		{"Example.com/library/mistral:latest+Q4_0", "example.com/library/_:latest", "mistral", false},
		{"example.com/Library/mistral:latest+Q4_0", "example.com/library/_:latest", "mistral", false},
		{"example.com/library/Mistral:latest+Q4_0", "example.com/library/_:latest", "Mistral", false},
		{"example.com/library/mistral:Latest+Q4_0", "example.com/library/_:latest", "mistral", false},
		{"example.com/library/mistral:Latest+q4_0", "example.com/library/_:latest", "mistral", false},

		// invalid mask
		{"example.com/library/mistral:latest+Q4_0", "example.com/mistral", "", true},

		// DefaultMask
		{"registry.ollama.ai/library/mistral:latest+Q4_0", DefaultMask, "mistral", false},

		// Auto-Fill
		{"x", "example.com/library/_:latest", "x", false},
		{"x", "example.com/library/_:latest+Q4_0", "x", false},
		{"x/y:z", "a.com/library/_:latest+Q4_0", "x/y:z", false},
		{"x/y:z", "a.com/library/_:latest+Q4_0", "x/y:z", false},
	}

	for _, tt := range cases {
		t.Run("", func(t *testing.T) {
			defer func() {
				if tt.wantPanic {
					if recover() == nil {
						t.Errorf("expected panic")
					}
				}
			}()

			p := ParseNameFill(tt.in, "")
			t.Logf("ParseName(%q) = %#v", tt.in, p)
			if g := p.DisplayShortest(tt.mask); g != tt.want {
				t.Errorf("got = %q; want %q", g, tt.want)
			}
		})
	}
}

func TestParseNameAllocs(t *testing.T) {
	allocs := testing.AllocsPerRun(1000, func() {
		keep(ParseNameFill("example.com/mistral:7b+Q4_0", ""))
	})
	if allocs > 0 {
		t.Errorf("ParseName allocs = %v; want 0", allocs)
	}
}

func BenchmarkParseName(b *testing.B) {
	b.ReportAllocs()

	for range b.N {
		keep(ParseNameFill("example.com/mistral:7b+Q4_0", ""))
	}
}

func FuzzParseName(f *testing.F) {
	f.Add("example.com/mistral:7b+Q4_0")
	f.Add("example.com/mistral:7b+q4_0")
	f.Add("example.com/mistral:7b+x")
	f.Add("x/y/z:8n+I")
	f.Add(":x")
	f.Add("@sha256-123456")
	f.Add("example.com/mistral:latest+Q4_0@sha256-123456")
	f.Add(":@!@")
	f.Add("...")
	f.Fuzz(func(t *testing.T, s string) {
		r0 := ParseNameFill(s, "")

		if strings.Contains(s, "..") && !r0.IsZero() {
			t.Fatalf("non-zero value for path with '..': %q", s)
		}

		if !r0.IsValid() && !r0.IsResolved() {
			if !r0.EqualFold(Name{}) {
				t.Errorf("expected invalid path to be zero value; got %#v", r0)
			}
			t.Skipf("invalid path: %q", s)
		}

		for _, p := range r0.Parts() {
			if len(p) > MaxNamePartLen {
				t.Errorf("part too long: %q", p)
			}
		}

		if !strings.EqualFold(r0.String(), s) {
			t.Errorf("String() did not round-trip with case insensitivity: %q\ngot  = %q\nwant = %q", s, r0.String(), s)
		}

		r1 := ParseNameFill(r0.String(), "")
		if !r0.EqualFold(r1) {
			t.Errorf("round-trip mismatch: %+v != %+v", r0, r1)
		}
	})
}

func TestFill(t *testing.T) {
	cases := []struct {
		dst  string
		src  string
		want string
	}{
		{"mistral", "o.com/library/PLACEHOLDER:latest+Q4_0", "o.com/library/mistral:latest+Q4_0"},
		{"o.com/library/mistral", "PLACEHOLDER:latest+Q4_0", "o.com/library/mistral:latest+Q4_0"},
		{"", "o.com/library/mistral:latest+Q4_0", "o.com/library/mistral:latest+Q4_0"},
	}

	for _, tt := range cases {
		t.Run(tt.dst, func(t *testing.T) {
			r := Fill(ParseNameFill(tt.dst, ""), ParseNameFill(tt.src, ""))
			if r.String() != tt.want {
				t.Errorf("Fill(%q, %q) = %q; want %q", tt.dst, tt.src, r, tt.want)
			}
		})
	}
}

func TestNameStringAllocs(t *testing.T) {
	name := ParseNameFill("example.com/ns/mistral:latest+Q4_0", "")
	allocs := testing.AllocsPerRun(1000, func() {
		keep(name.String())
	})
	if allocs > 1 {
		t.Errorf("String allocs = %v; want 0", allocs)
	}
}

func ExampleFill() {
	defaults := ParseNameFill("registry.ollama.com/library/PLACEHOLDER:latest+Q4_0", "")
	r := Fill(ParseNameFill("mistral", ""), defaults)
	fmt.Println(r)

	// Output:
	// registry.ollama.com/library/mistral:latest+Q4_0
}

func ExampleName_MapHash() {
	m := map[uint64]bool{}

	// key 1
	m[ParseNameFill("mistral:latest+q4", "").MapHash()] = true
	m[ParseNameFill("miSTRal:latest+Q4", "").MapHash()] = true
	m[ParseNameFill("mistral:LATest+Q4", "").MapHash()] = true

	// key 2
	m[ParseNameFill("mistral:LATest", "").MapHash()] = true

	fmt.Println(len(m))
	// Output:
	// 2
}

func ExampleName_CompareFold_sort() {
	names := []Name{
		ParseNameFill("mistral:latest", ""),
		ParseNameFill("mistRal:7b+q4", ""),
		ParseNameFill("MIstral:7b", ""),
	}

	slices.SortFunc(names, Name.CompareFold)

	for _, n := range names {
		fmt.Println(n)
	}

	// Output:
	// MIstral:7b
	// mistRal:7b+q4
	// mistral:latest
}

func ExampleName_completeAndResolved() {
	for _, s := range []string{
		"x/y/z:latest+q4_0@sha123-1",
		"x/y/z:latest+q4_0",
		"@sha123-1",
	} {
		name := ParseNameFill(s, "")
		fmt.Printf("complete:%v resolved:%v  digest:%s\n", name.IsComplete(), name.IsResolved(), name.Digest())
	}

	// Output:
	// complete:true resolved:true  digest:sha123-1
	// complete:true resolved:false  digest:
	// complete:false resolved:true  digest:sha123-1
}

func ExampleName_DisplayShortest() {
	name := ParseNameFill("example.com/jmorganca/mistral:latest+Q4_0", "")

	fmt.Println(name.DisplayShortest("example.com/jmorganca/_:latest"))
	fmt.Println(name.DisplayShortest("example.com/_/_:latest"))
	fmt.Println(name.DisplayShortest("example.com/_/_:_"))
	fmt.Println(name.DisplayShortest("_/_/_:_"))

	// Default
	name = ParseNameFill("registry.ollama.ai/library/mistral:latest+Q4_0", "")
	fmt.Println(name.DisplayShortest(""))

	// Output:
	// mistral
	// jmorganca/mistral
	// jmorganca/mistral:latest
	// example.com/jmorganca/mistral:latest
	// mistral
}

func keep[T any](v T) T { return v }
