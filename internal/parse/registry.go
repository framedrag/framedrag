package parse

import (
	"fmt"
	"sort"
)

// Options carries per-feed parser options from the catalog/overlay.
type Options struct {
	// CSVColumn is the zero-based index of the address column for the
	// csv parser. The detect parser uses it as a fallback when column
	// sniffing is inconclusive.
	CSVColumn int
}

// factory builds a parser configured for one feed.
type factory func(Options) Parser

var registry = map[string]factory{}

// Register makes a parser constructor available under name. It is
// meant to be called from init; registering a duplicate name panics.
func Register(name string, f factory) {
	if _, dup := registry[name]; dup {
		panic(fmt.Sprintf("parse: duplicate parser %q", name))
	}
	registry[name] = f
}

// Get returns the parser registered under name, configured with the
// given per-feed options.
func Get(name string, opts Options) (Parser, error) {
	f, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("parse: unknown parser %q (have %v)", name, Names())
	}
	return f(opts), nil
}

// Names returns the registered parser names, sorted.
func Names() []string {
	names := make([]string, 0, len(registry))
	for n := range registry {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func init() {
	Register("plain", func(Options) Parser { return plainParser{} })
	Register("range", func(Options) Parser { return rangeParser{} })
	Register("csv", func(o Options) Parser { return NewCSV(o.CSVColumn) })
	Register("spamhaus", func(Options) Parser { return spamhausParser{} })
	Register("detect", func(o Options) Parser { return NewDetect(o) })
}
