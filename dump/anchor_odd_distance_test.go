package dump

import "testing"

// TestAnchorKindOKRefusesOddDistance pins the odd-distance conjunct in
// anchorKindOK: a path whose kind-to-Ext distance is odd must not anchor,
// so the wrapper segment keeps the key rather than being read as an object name.
func TestAnchorKindOKRefusesOddDistance(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{"wrapper before a plain object form module",
			"wrap/Catalogs/Спр1/Forms/Ext/Module.bsl", "wrap.Catalogs.МодульФормы"},
		{"wrapper before a plain object's own module",
			"Обёртка/Catalogs/Объект/Forms/Ext/ObjectModule.bsl", "Обёртка.Catalogs.МодульОбъекта"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := bslPathToModuleName(tt.path); got != tt.want {
				t.Errorf("bslPathToModuleName(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}
