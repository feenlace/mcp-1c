package dump

import "fmt"

// DefaultExtensionDeepScanLimit is the number of eligible directory manifest
// probes available to deep extension discovery after the shallow scan.
const DefaultExtensionDeepScanLimit = 64

// IndexOptions controls dump indexing and its cache identity. The zero value
// uses the default extension discovery limit and the legacy cache path.
type IndexOptions struct {
	extensionDeepScanLimit int
}

// NewIndexOptions selects a positive limit for deep extension discovery.
func NewIndexOptions(extensionDeepScanLimit int) (IndexOptions, error) {
	if extensionDeepScanLimit <= 0 {
		return IndexOptions{}, fmt.Errorf("expected a positive integer, got %d", extensionDeepScanLimit)
	}
	return IndexOptions{extensionDeepScanLimit: extensionDeepScanLimit}, nil
}

func (opts IndexOptions) deepScanLimit() int {
	if opts.extensionDeepScanLimit == 0 {
		return DefaultExtensionDeepScanLimit
	}
	return opts.extensionDeepScanLimit
}
